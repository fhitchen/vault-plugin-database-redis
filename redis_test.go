// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package redis

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/vault/sdk/database/dbplugin/v5"
	"github.com/mediocregopher/radix/v4"
	"github.com/ory/dockertest/v3"
	dc "github.com/ory/dockertest/v3/docker"
)

const (
	defaultUsername = "default"
	defaultPassword = "default-pa55w0rd"
	adminUsername   = "Administrator"
	adminPassword   = "password"
	aclCat          = "+@admin"
	testRedisRole   = `["%s"]`
	testRedisGroup  = `["+@all"]`
	testRedisRole3  = `["%s", "%s", "%s"]`
)

var (
	redisTls                   = false
	redis_container            = false
	redis_secondaries          = ""
	redis_cluster_hosts        = ""
	redis_sentinel_hosts       = ""
	redis_sentinel_master_name = ""
	persistence_mode           = ""
)

func prepareRedisTestContainer(t *testing.T) (func(), string, int) {
	if os.Getenv("TEST_REDIS_TLS") != "" {
		redisTls = true
	}
	if env := os.Getenv("TEST_REDIS_HOST"); env != "" {
		redis_secondaries = os.Getenv("TEST_REDIS_SECONDARIES")
		port, err := strconv.Atoi(os.Getenv("TEST_REDIS_PORT"))
		if err != nil {
			port = 6379
		}
		return func() {}, env, port
	}
	if env := os.Getenv("TEST_REDIS_CLUSTER"); env != "" {
		redis_cluster_hosts = env
		return func() {}, env, -1
	}

	if env := os.Getenv("TEST_REDIS_SENTINELS"); env != "" {
		redis_sentinel_hosts = env
		env = os.Getenv("TEST_REDIS_SENTINEL_MASTER_NAME")
		if env != "" {
			redis_sentinel_master_name = env
		}
		return func() {}, env, -2
	}

	// redver should match a redis repository tag. Default to latest.
	redver := os.Getenv("REDIS_VERSION")
	if redver == "" {
		redver = "latest"
	}

	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("Failed to connect to docker: %s", err)
	}

	ro := &dockertest.RunOptions{
		Repository:   "docker.io/redis",
		Tag:          redver,
		ExposedPorts: []string{"6379"},
		PortBindings: map[dc.Port][]dc.PortBinding{
			"6379": {
				{HostIP: "0.0.0.0", HostPort: "6379"},
			},
		},
	}
	resource, err := pool.RunWithOptions(ro)
	if err != nil {
		t.Fatalf("Could not start local redis docker container: %s", err)
	}

	cleanup := func() {
		err := pool.Retry(func() error {
			return pool.Purge(resource)
		})
		if err != nil {
			if strings.Contains(err.Error(), "No such container") {
				return
			}
			t.Fatalf("Failed to cleanup local container: %s", err)
		}
	}

	address := "127.0.0.1:6379"

	if err = pool.Retry(func() error {
		t.Log("Waiting for the database to start...")
		poolConfig := radix.PoolConfig{}
		_, err := poolConfig.New(context.Background(), "tcp", address)
		if err != nil {
			return err
		}

		return nil
	}); err != nil {
		t.Fatalf("Could not connect to redis: %s", err)
		cleanup()
	}
	time.Sleep(3 * time.Second)
	redis_container = true
	return cleanup, "0.0.0.0", 6379
}

func TestDriver(t *testing.T) {
	var err error
	// var caCert []byte
	if os.Getenv("TEST_REDIS_TLS") != "" {
		caCertFile := os.Getenv("CA_CERT_FILE")
		_, err = os.ReadFile(caCertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", caCertFile, err))
		}
	}

	// Spin up redis
	cleanup, host, port := prepareRedisTestContainer(t)
	defer cleanup()

	// We are testing sentinel or cluster so clear host.
	if port < 0 {
		host = ""
	}

	err, persistence_mode = checkPersistenceMode(host, port, defaultUsername, defaultPassword)
	if err != nil {
		t.Fatalf("Failed to check persistence mode: %s", err)
	}

	err = createUser(host, port, defaultUsername, defaultPassword, "Administrator", "password", aclCat)
	if err != nil {
		t.Fatalf("Failed to create Administrator user using 'default' user: %s", err)
	}
	err = createUser(host, port, adminUsername, adminPassword, "rotate-root", "rotate-rootpassword", aclCat)
	if err != nil {
		t.Fatalf("Failed to create rotate-root test user: %s", err)
	}
	err = createUser(host, port, adminUsername, adminPassword, "vault-edu", "password", aclCat)
	if err != nil {
		t.Fatalf("Failed to create vault-edu test user: %s", err)
	}

	t.Run("Init", func(t *testing.T) { testRedisDBInitialize_NoTLS(t, host, port) })
	t.Run("Init", func(t *testing.T) { testRedisDBInitialize_persistence(t, host, port) })
	t.Run("Init", func(t *testing.T) { testRedisDBInitialize_TLS(t, host, port) })
	t.Run("Create/Revoke", func(t *testing.T) { testRedisDBCreateUser(t, host, port) })
	t.Run("Create/Revoke", func(t *testing.T) { testRedisDBCreateUser_DefaultRule(t, host, port) })
	t.Run("Create/Revoke", func(t *testing.T) { testRedisDBCreateUser_plusRole(t, host, port) })
	t.Run("Create/Revoke", func(t *testing.T) { testRedisDBCreateUser_groupOnly(t, host, port) })
	t.Run("Create/Revoke", func(t *testing.T) { testRedisDBCreateUser_roleAndGroup(t, host, port) })
	t.Run("Create/Revoke", func(t *testing.T) { testRedisDBCreateUser_persistAclFile(t, host, port) })
	// t.Run("Create/Revoke", func(t *testing.T) { testRedisDBCreate_persistConfig(t, host, port) })
	t.Run("Rotate", func(t *testing.T) { testRedisDBRotateRootCredentials(t, host, port) })
	t.Run("Creds", func(t *testing.T) { testRedisDBSetCredentials(t, host, port) })
	t.Run("Secret", func(t *testing.T) { testConnectionProducerSecretValues(t) })
	t.Run("TimeoutCalc", func(t *testing.T) { testComputeTimeout(t) })
}

func setupRedisDBInitialize(t *testing.T, connectionDetails map[string]interface{}) (err error) {
	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err = db.Initialize(context.Background(), initReq)
	if err != nil {
		return err
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	err = db.Close()
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	return nil
}

func testRedisDBInitialize_NoTLS(t *testing.T, host string, port int) {
	if redisTls {
		t.Skip("skipping plain text Init() test in TLS mode")
	}

	t.Log("Testing plain text Init()")

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
	}
	err := setupRedisDBInitialize(t, connectionDetails)
	if err != nil {
		t.Fatalf("Testing Init() failed: error: %s", err)
	}
}

func testRedisDBInitialize_TLS(t *testing.T, host string, port int) {
	if !redisTls {
		t.Skip("skipping TLS Init() test in plain text mode")
	}

	CACertFile := os.Getenv("CA_CERT_FILE")
	CACert, err := os.ReadFile(CACertFile)
	if err != nil {
		t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
	}

	t.Log("Testing TLS Init()")

	var cluster_hosts string

	if port == -1 {
		cluster_hosts = host
		host = ""
	}

	connectionDetails := map[string]interface{}{
		"host":        host,
		"port":        port,
		"secondaries": redis_secondaries,
		"cluster":     cluster_hosts,
		"username":    adminUsername,
		"password":    adminPassword,
		"tls":         true,
		"cacrt":       CACert,
	}
	err = setupRedisDBInitialize(t, connectionDetails)
	if err != nil {
		t.Fatalf("Testing TLS Init() failed: error: %s", err)
	}
}

func testRedisDBInitialize_persistence(t *testing.T, host string, port int) {
	if redisTls {
		t.Skip("skipping plain text Init() with persistence_mode test in TLS mode")
	}

	t.Log("Testing plain text Init() with persistence_mode")

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
		"persistence_mode":     "garbage",
	}

	err := setupRedisDBInitialize(t, connectionDetails)

	if err == nil {
		t.Fatalf("Testing Init() should have failed as the perstence_mode is garbage.")
	}

	connectionDetails = map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
		"persistence_mode":     "rewrite",
	}

	err = setupRedisDBInitialize(t, connectionDetails)
	if err != nil {
		t.Fatalf("Testing Init() with perstence_mode rewrite failed: %s.", err)
	}

	connectionDetails = map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
		"persistence_mode":     "aclfile",
	}

	err = setupRedisDBInitialize(t, connectionDetails)
	if err != nil {
		t.Fatalf("Testing Init() with perstence_mode aclfile failed: %s", err)
	}
}

func testRedisDBCreateUser(t *testing.T, address string, port int) {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	t.Log("Testing CreateUser()")

	host := address
	var rule []string

	if len(redis_cluster_hosts) != 0 {
		rule = []string{`["+readonly", "+cluster"]`}
	}

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("Failed to initialize database: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	password := "y8fva_sdVA3rasf"

	createReq := dbplugin.NewUserRequest{
		UsernameConfig: dbplugin.UsernameMetadata{
			DisplayName: "test",
			RoleName:    "test",
		},
		Statements: dbplugin.Statements{
			Commands: rule,
		},
		Password:   password,
		Expiration: time.Now().Add(time.Minute),
	}

	userResp, err := db.NewUser(context.Background(), createReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	db.Close()

	if err := checkCredsExist(t, userResp.Username, password, address, port); err != nil {
		t.Fatalf("Could not connect with new credentials: %s", err)
	}

	err = revokeUser(t, userResp.Username, address, port)
	if err != nil {
		t.Fatalf("Could not revoke user: %s", userResp.Username)
	}
}

func checkCredsExist(t *testing.T, username, password, address string, port int) error {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	t.Log("Testing checkCredsExist()")

	host := address

	if port < 0 {
		host = ""
	}

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             username,
		"password":             password,
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()

	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	return nil
}

func checkRuleAllowed(t *testing.T, username, password, address string, port int, cmd string, rules []string) error {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	t.Log("Testing checkRuleAllowed()")

	host := address

	if port < 0 {
		host = ""
	}

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             username,
		"password":             password,
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}
	var response string
	err = db.client.Do(context.Background(), radix.Cmd(&response, cmd, rules...))
	if err != nil {
		return fmt.Errorf("Response in checkRules for %s %w", response, err)
	}

	return err
}

func revokeUser(t *testing.T, username, address string, port int) error {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	t.Log("Testing RevokeUser()")

	host := address

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	delUserReq := dbplugin.DeleteUserRequest{Username: username}

	_, err = db.DeleteUser(context.Background(), delUserReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	return nil
}

func testRedisDBCreateUser_DefaultRule(t *testing.T, address string, port int) {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	t.Log("Testing CreateUser_DefaultRule()")

	host := address

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	username := "test"
	password := "y8fva_sdVA3rasf"

	createReq := dbplugin.NewUserRequest{
		UsernameConfig: dbplugin.UsernameMetadata{
			DisplayName: username,
			RoleName:    username,
		},
		Statements: dbplugin.Statements{
			Commands: []string{`["~foo", "+@read", "+readonly", "+cluster"]`},
		},
		Password:   password,
		Expiration: time.Now().Add(time.Minute * 5),
	}

	userResp, err := db.NewUser(context.Background(), createReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if err := checkCredsExist(t, userResp.Username, password, address, port); err != nil {
		t.Fatalf("Could not connect with new credentials: %s", err)
	}
	rules := []string{"foo"}
	if err := checkRuleAllowed(t, userResp.Username, password, address, port, "get", rules); err != nil {
		t.Fatalf("get failed for user %s with +@read rule: %s", userResp.Username, err)
	}

	rules = []string{"foo", "bar"}
	if err = checkRuleAllowed(t, userResp.Username, password, address, port, "set", rules); err == nil {
		t.Fatalf("set did not fail user %s with +@read rule: %s", userResp.Username, err)
	}

	err = revokeUser(t, userResp.Username, address, port)
	if err != nil {
		t.Fatalf("Could not revoke user: %s", userResp.Username)
	}

	db.Close()
}

func testRedisDBCreateUser_plusRole(t *testing.T, address string, port int) {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	t.Log("Testing CreateUser_plusRole()")

	host := address

	if port < 0 {
		host = ""
	}

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	password := "y8fva_sdVA3rasf"

	createReq := dbplugin.NewUserRequest{
		UsernameConfig: dbplugin.UsernameMetadata{
			DisplayName: "test",
			RoleName:    "test",
		},
		Statements: dbplugin.Statements{
			Commands: []string{fmt.Sprintf(testRedisRole, "+@all")},
		},
		Password:   password,
		Expiration: time.Now().Add(time.Minute),
	}

	userResp, err := db.NewUser(context.Background(), createReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	db.Close()

	if err := checkCredsExist(t, userResp.Username, password, address, port); err != nil {
		t.Fatalf("Could not connect with new credentials: %s", err)
	}

	err = revokeUser(t, userResp.Username, address, port)
	if err != nil {
		t.Fatalf("Could not revoke user: %s", userResp.Username)
	}
}

func testRedisDBCreateUser_groupOnly(t *testing.T, address string, port int) {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	host := address

	if port < 0 {
		host = ""
	}

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	password := "y8fva_sdVA3rasf"

	createReq := dbplugin.NewUserRequest{
		UsernameConfig: dbplugin.UsernameMetadata{
			DisplayName: "test",
			RoleName:    "test",
		},
		Statements: dbplugin.Statements{
			Commands: []string{fmt.Sprintf(testRedisGroup)},
		},
		Password:   password,
		Expiration: time.Now().Add(time.Minute),
	}

	userResp, err := db.NewUser(context.Background(), createReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	db.Close()

	if err := checkCredsExist(t, userResp.Username, password, address, port); err != nil {
		t.Fatalf("Could not connect with new credentials: %s", err)
	}

	err = revokeUser(t, userResp.Username, address, port)
	if err != nil {
		t.Fatalf("Could not revoke user: %s", userResp.Username)
	}
}

func testRedisDBCreateUser_roleAndGroup(t *testing.T, address string, port int) {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	host := address

	if port == -1 {
		host = ""
	}
	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	password := "y8fva_sdVA3rasf"

	createReq := dbplugin.NewUserRequest{
		UsernameConfig: dbplugin.UsernameMetadata{
			DisplayName: "test",
			RoleName:    "test",
		},
		Statements: dbplugin.Statements{
			Commands: []string{fmt.Sprintf(testRedisRole3, aclCat, "+readonly", "+cluster")},
		},
		Password:   password,
		Expiration: time.Now().Add(time.Minute),
	}

	userResp, err := db.NewUser(context.Background(), createReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	db.Close()

	if err := checkCredsExist(t, userResp.Username, password, address, port); err != nil {
		t.Fatalf("Could not connect with new credentials: %s", err)
	}

	err = revokeUser(t, userResp.Username, address, port)
	if err != nil {
		t.Fatalf("Could not revoke user: %s", userResp.Username)
	}
}

func testRedisDBCreateUser_persistAclFile(t *testing.T, address string, port int) {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	if redis_container == true {
		t.Skip("Skipping persist config as REDIS container is not configured to use an acl file.")
	}

	host := address

	if port < 0 {
		host = ""
	}

	t.Log("Testing CreateUser_persist()")

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
		"persistence_mode":     "aclfile",
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	password := "y8fva_sdVA3rasf"

	createReq := dbplugin.NewUserRequest{
		UsernameConfig: dbplugin.UsernameMetadata{
			DisplayName: "test",
			RoleName:    "test",
		},
		Statements: dbplugin.Statements{
			Commands: []string{fmt.Sprintf(testRedisGroup)},
		},
		Password:   password,
		Expiration: time.Now().Add(time.Minute),
	}

	userResp, err := db.NewUser(context.Background(), createReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	db.Close()

	if err := checkCredsExist(t, userResp.Username, password, address, port); err != nil {
		t.Fatalf("Could not connect with new credentials: %s", err)
	}

	err = revokeUser(t, userResp.Username, address, port)
	if err != nil {
		t.Fatalf("Could not revoke user: %s", userResp.Username)
	}
}

func testRedisDBRotateRootCredentials(t *testing.T, address string, port int) {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	t.Log("Testing RotateRootCredentials()")

	host := address

	if port < 0 {
		host = ""
	}

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             "rotate-root",
		"password":             "rotate-rootpassword",
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	defer db.Close()

	updateReq := dbplugin.UpdateUserRequest{
		Username: "rotate-root",
		Password: &dbplugin.ChangePassword{
			NewPassword: "newpassword",
		},
	}

	_, err = db.UpdateUser(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	// defer setting the password back in case the test fails.
	defer doRedisDBSetCredentials(t, "rotate-root", "rotate-rootpassword", address, port)

	if err := checkCredsExist(t, db.Username, "newpassword", address, port); err != nil {
		t.Fatalf("Could not connect with new RotatedRootcredentials: %s", err)
	}
}

func doRedisDBSetCredentials(t *testing.T, username, password, address string, port int) {
	t.Log("Testing SetCredentials()")

	host := address

	if port < 0 {
		host = ""
	}

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			t.Fatal(fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err))
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err := db.Initialize(context.Background(), initReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	if !db.Initialized {
		t.Fatal("Database should be initialized")
	}

	// test that SetCredentials fails if the user does not exist...
	updateReq := dbplugin.UpdateUserRequest{
		Username: "userThatDoesNotExist",
		Password: &dbplugin.ChangePassword{
			NewPassword: "goodPassword",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5000*time.Millisecond)
	defer cancel()
	_, err = db.UpdateUser(ctx, updateReq)
	if err == nil {
		t.Fatalf("err: did not error on setting password for userThatDoesNotExist")
	}

	updateReq = dbplugin.UpdateUserRequest{
		Username: username,
		Password: &dbplugin.ChangePassword{
			NewPassword: password,
		},
	}

	_, err = db.UpdateUser(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	db.Close()

	if err := checkCredsExist(t, username, password, address, port); err != nil {
		t.Fatalf("Could not connect with rotated credentials: %s", err)
	}
}

func testRedisDBSetCredentials(t *testing.T, host string, port int) {
	if os.Getenv("VAULT_ACC") == "" {
		t.SkipNow()
	}

	doRedisDBSetCredentials(t, "vault-edu", "password", host, port)
}

func testConnectionProducerSecretValues(t *testing.T) {
	t.Log("Testing redisDBConnectionProducer.secretValues()")

	cp := &redisDBConnectionProducer{
		Username: "USR",
		Password: "PWD",
	}

	if cp.secretValues()["USR"] != "[username]" &&
		cp.secretValues()["PWD"] != "[password]" {
		t.Fatal("redisDBConnectionProducer.secretValues() test failed.")
	}
}

func testComputeTimeout(t *testing.T) {
	t.Log("Testing computeTimeout")
	if computeTimeout(context.Background()) != defaultTimeout {
		t.Fatalf("Background timeout not set to %s milliseconds.", defaultTimeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if computeTimeout(ctx) == defaultTimeout {
		t.Fatal("WithTimeout failed")
	}
}

func checkPersistenceMode(address string, port int, adminUsername, adminPassword string) (err error, mode string) {
	fmt.Printf("Checking the supported persistence mode.\n")

	host := address
	//	var cluster_rules []string

	if port == -1 {
		//		cluster_rules = []string{"+readonly", "+cluster"}
	}

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
	}
	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			return fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err), ""
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err = db.Initialize(context.Background(), initReq)
	if err != nil {
		return fmt.Errorf("Failed to initialize database: %s", err), ""
	}

	if !db.Initialized {
		return fmt.Errorf("Database should be initialized"), ""
	}

	// setup REDIS command
	//	aclargs := []string{"SETUSER", username, "ON", ">" + password, aclRule}
	//	aclargs = append(aclargs, cluster_rules...)

	var replicaSets map[string]radix.ReplicaSet
	var connType string

	switch db.client.(type) {

	case *radix.Sentinel:
		replicaSets, err = db.client.(*radix.Sentinel).Clients()
		connType = "Sentinel"

	case radix.MultiClient:
		replicaSets, err = db.client.Clients()
		connType = "MultiClient"

	case *radix.Cluster:
		replicaSets, err = db.client.(*radix.Cluster).Clients()
		connType = "Cluster"
	}
	if err != nil {
		return fmt.Errorf("retrieving %s clients failed error: %w", connType, err), ""
	}

	ctx, _ := context.WithTimeout(context.Background(), 5000*time.Millisecond)
	var response []string
	mb := radix.Maybe{Rcv: &response}
	// var redisErr resp3.SimpleError

	for _, rs := range replicaSets {
		for _, v := range getClientsFromRS(rs) {
			err = v.Do(ctx, radix.Cmd(&mb, "CONFIG", "GET", "ACLFILE"))
			if err != nil {
				return err, ""
			} else if mb.Null {
				fmt.Printf("ACLFILE NOT SET\n")
			} else {
				fmt.Printf("ACLFILE IS SET %#v on node %s\n", response, v.Addr().String())
			}
		}
	}

	if db.client != nil {
		if err = db.client.Close(); err != nil {
			return err, ""
		}
	}

	return err, "some mode"
}

func createUser(address string, port int, adminUsername, adminPassword, username, password, aclRule string) (err error) {
	fmt.Printf("Creating test user %s\n", username)
	host := address

	var cluster_rules []string
	// extra rules needed to access cluster information
	if len(redis_cluster_hosts) != 0 {
		cluster_rules = []string{"+readonly", "+cluster"}
	}

	connectionDetails := map[string]interface{}{
		"host":                 host,
		"port":                 port,
		"secondaries":          redis_secondaries,
		"cluster":              redis_cluster_hosts,
		"sentinels":            redis_sentinel_hosts,
		"sentinel_master_name": redis_sentinel_master_name,
		"username":             adminUsername,
		"password":             adminPassword,
	}

	if redisTls {
		CACertFile := os.Getenv("CA_CERT_FILE")
		CACert, err := os.ReadFile(CACertFile)
		if err != nil {
			return fmt.Errorf("unable to read CA_CERT_FILE at %v: %w", CACertFile, err)
		}

		connectionDetails["tls"] = true
		connectionDetails["ca_cert"] = CACert
		connectionDetails["insecure_tls"] = true
	}

	initReq := dbplugin.InitializeRequest{
		Config:           connectionDetails,
		VerifyConnection: true,
	}

	db := new()
	_, err = db.Initialize(context.Background(), initReq)
	if err != nil {
		return fmt.Errorf("Failed to initialize database: %s", err)
	}

	if !db.Initialized {
		return fmt.Errorf("Database should be initialized")
	}

	// setup REDIS command
	aclargs := []string{"SETUSER", username, "ON", ">" + password, aclRule}
	aclargs = append(aclargs, cluster_rules...)

	var response string
	var replicaSets map[string]radix.ReplicaSet
	var connType string

	switch db.client.(type) {

	case *radix.Sentinel:
		replicaSets, err = db.client.(*radix.Sentinel).Clients()
		connType = "Sentinel"

	case radix.MultiClient:
		replicaSets, err = db.client.Clients()
		connType = "MultiClient"

	case *radix.Cluster:
		replicaSets, err = db.client.(*radix.Cluster).Clients()
		connType = "Cluster"
	}
	if err != nil {
		return fmt.Errorf("retrieving %s clients failed error: %w", connType, err)
	}

	ctx, _ := context.WithTimeout(context.Background(), 5000*time.Millisecond)

	for node, rs := range replicaSets {
		for _, v := range getClientsFromRS(rs) {

			err = v.Do(ctx, radix.Cmd(&response, "ACL", aclargs...))
			if err != nil {
				return fmt.Errorf("Response in %s newUser: %s for node %s, error: %w", connType, node, response, err)
			}
		}
	}

	return nil
}
