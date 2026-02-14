package conf

import (
	"path/filepath"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	"github.com/go-ini/ini"
	"github.com/stretchr/testify/assert"
	"os"
	"testing"
)

func TestNewIniConfigProviderCreatesDefaultWhenMissing(t *testing.T) {
	asserts := assert.New(t)
	logger := logging.NewConsoleLogger(logging.LevelError)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "conf.ini")

	provider, err := NewIniConfigProvider(configPath, logger)
	asserts.NoError(err)
	asserts.NotNil(provider)
	asserts.True(util.Exists(configPath))
}

func TestNewIniConfigProviderInvalidIniReturnsError(t *testing.T) {
	asserts := assert.New(t)
	logger := logging.NewConsoleLogger(logging.LevelError)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "conf.ini")

	testCase := `[Database]
Type = mysql
User = root
Password233root
Host = 127.0.0.1:3306
Name = v3
TablePrefix = v3_`
	err := os.WriteFile(configPath, []byte(testCase), 0644)
	if err != nil {
		panic(err)
	}

	provider, err := NewIniConfigProvider(configPath, logger)
	asserts.Error(err)
	asserts.Nil(provider)
}

func TestNewIniConfigProviderValidIniNoError(t *testing.T) {
	asserts := assert.New(t)
	logger := logging.NewConsoleLogger(logging.LevelError)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "conf.ini")

	testCase := `
[System]
Listen = 3000
Mode = master
HashIDSalt = 1

[Database]
Type = mysql
User = root
Password = root
Host = 127.0.0.1:3306
Name = v3
TablePrefix = v3_`
	err := os.WriteFile(configPath, []byte(testCase), 0644)
	if err != nil {
		panic(err)
	}

	provider, err := NewIniConfigProvider(configPath, logger)
	asserts.NoError(err)
	asserts.NotNil(provider)
	asserts.Equal("3000", provider.System().Listen)
}

func TestMapSection(t *testing.T) {
	asserts := assert.New(t)
	logger := logging.NewConsoleLogger(logging.LevelError)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "conf.ini")

	testCase := `
[System]
Listen = 3000
Mode = master
HashIDSalt = 1

[Database]
Type = mysql
User = root
Password:root
Host = 127.0.0.1:3306
Name = v3
TablePrefix = v3_`
	err := os.WriteFile(configPath, []byte(testCase), 0644)
	if err != nil {
		panic(err)
	}

	provider, err := NewIniConfigProvider(configPath, logger)
	asserts.NoError(err)
	asserts.NotNil(provider)

	cfg, err := ini.Load(configPath)
	asserts.NoError(err)

	var db Database
	err = mapSection(cfg, "Database", &db)
	asserts.NoError(err)
	asserts.Equal(MySqlDB, db.Type)
	asserts.Equal("root", db.Password)

	sys := *SystemConfig
	err = mapSection(cfg, "System", &sys)
	asserts.NoError(err)
	asserts.Equal("3000", sys.Listen)
	asserts.Equal(MasterMode, sys.Mode)

}
