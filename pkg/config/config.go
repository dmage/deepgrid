package config

import (
	"io/ioutil"
	"os"

	"sigs.k8s.io/yaml"
)

type TestGroup struct {
	GCSPrefix string `json:"gcs_prefix"`
	Name      string `json:"name"`
}

type Config struct {
	TestGroups []TestGroup `json:"test_groups"`
}

func LoadFromFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf, err := ioutil.ReadAll(f)
	if err != nil {
		return nil, err
	}

	config := &Config{}
	err = yaml.Unmarshal(buf, config)
	return config, err
}
