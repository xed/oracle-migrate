package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"os"

	_ "github.com/mattn/go-oci8"
	"gopkg.in/yaml.v2"
)

//
//var dialects = map[string]gorp.Dialect{
//	"sqlite3":  gorp.SqliteDialect{},
//	"postgres": gorp.PostgresDialect{},
//	"mysql":    gorp.MySQLDialect{Engine: "InnoDB", Encoding: "UTF8"},
//}

var ConfigFile string
var ConfigEnvironment string

func ConfigFlags(f *flag.FlagSet) {
	f.StringVar(&ConfigFile, "config", "dbconfig.yml", "Configuration file to use.")
	f.StringVar(&ConfigEnvironment, "env", "development", "Environment to use.")
}

type Environment struct {
	//Dialect    string `yaml:"dialect"`
	DataSource string `yaml:"datasource"`
	Dir        string `yaml:"dir"`
	TableName  string `yaml:"table"`
	SchemaName string `yaml:"schema"`
}

func ReadConfig() (map[string]*Environment, error) {
	file, err := ioutil.ReadFile(ConfigFile)
	if err != nil {
		return nil, err
	}

	config := make(map[string]*Environment)
	err = yaml.Unmarshal(file, config)
	if err != nil {
		return nil, err
	}

	return config, nil
}

func GetEnvironment() (*Environment, error) {
	config, err := ReadConfig()
	if err != nil {
		return nil, err
	}

	env := config[ConfigEnvironment]
	if env == nil {
		return nil, errors.New("No environment: " + ConfigEnvironment)
	}

	//if env.Dialect == "" {
	//	return nil, errors.New("No dialect specified")
	//}

	if env.DataSource == "" {
		return nil, errors.New("No data source specified")
	}
	env.DataSource = os.ExpandEnv(env.DataSource)

	if env.Dir == "" {
		env.Dir = "migrations"
	}

	//if env.TableName != "" {
	//	migrate.SetTable(env.TableName)
	//}
	//
	//if env.SchemaName != "" {
	//	migrate.SetSchema(env.SchemaName)
	//}

	return env, nil
}

func GetConnection(env *Environment) (*sql.DB, string, error) {
	db, err := sql.Open("oci8", env.DataSource)
	if err != nil {
		fmt.Printf("Open error is not nil: %v", err)
		return nil, "", fmt.Errorf("Cannot connect to database: %s", err)
	}
	if db == nil {
		fmt.Println("db is nil")
		return nil, "", fmt.Errorf("Cannot connect to database: db is nil")
	}

	//defer func() {
	//	err = db.Close()
	//	if err != nil {
	//		fmt.Println("Close error is not nil:", err)
	//	}
	//}()

	return db, "oci8", nil
}
