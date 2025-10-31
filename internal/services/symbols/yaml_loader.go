package symbols

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// SymbolConfig represents the YAML configuration structure
type SymbolConfig struct {
	Symbols []string `yaml:"symbols"`
}

// LoadSymbolsFromYAML loads symbols from a YAML file
func LoadSymbolsFromYAML(filePath string) ([]string, error) {
	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read symbols file: %w", err)
	}

	// Parse YAML
	var config SymbolConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse symbols YAML: %w", err)
	}

	if len(config.Symbols) == 0 {
		return nil, fmt.Errorf("no symbols found in config file")
	}

	return config.Symbols, nil
}

// LoadSymbolsWithFallback tries to load from YAML, falls back to defaults
func LoadSymbolsWithFallback(filePath string) []string {
	symbols, err := LoadSymbolsFromYAML(filePath)
	if err != nil {
		// Return default symbols if file doesn't exist or has errors
		return DefaultSymbols
	}
	return symbols
}
