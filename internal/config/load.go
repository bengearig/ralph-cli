package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

//go:embed defaults.yaml
var defaultsYAML []byte

const (
	// EnvPrefix marks environment variables that map onto config keys.
	EnvPrefix = "RALPH_"
	// EnvNestingSeparator separates path segments in environment variable
	// names, since single underscores already appear inside key names.
	EnvNestingSeparator = "__"
	delim               = "."
)

// FlagKeys maps command-line flag names to the config keys they override.
var FlagKeys = map[string]string{
	"log-level": "log.level",
}

// Sources lists where configuration is read from. Empty file paths and a nil
// Environ or Flags skip that layer.
type Sources struct {
	GlobalFile  string
	ProjectFile string
	// ExtraFile must exist when set, because the user asked for it explicitly.
	ExtraFile string
	Environ   []string
	Flags     *pflag.FlagSet
}

// Layer describes one configuration source and whether it contributed.
type Layer struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Present bool   `json:"present"`
}

// Loaded is a validated configuration plus provenance of its layers.
type Loaded struct {
	Config *Config
	Layers []Layer
	k      *koanf.Koanf
}

// YAML renders the effective merged configuration.
func (l *Loaded) YAML() ([]byte, error) {
	return l.k.Marshal(yaml.Parser())
}

// GlobalPath returns the per-user config file location, honoring
// XDG_CONFIG_HOME on Linux.
func GlobalPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config directory: %w", err)
	}
	return filepath.Join(dir, "ralph", "config.yaml"), nil
}

// Defaults returns the built-in configuration.
func Defaults() (*Config, error) {
	l, err := Load(Sources{})
	if err != nil {
		return nil, err
	}
	return l.Config, nil
}

// Load merges every layer in precedence order, decodes strictly so unknown
// keys are reported, and validates the result.
func Load(src Sources) (*Loaded, error) {
	k := koanf.New(delim)
	defaults, err := yaml.Parser().Unmarshal(defaultsYAML)
	if err != nil {
		return nil, fmt.Errorf("parsing built-in defaults: %w", err)
	}
	if err := k.Load(confmap.Provider(defaults, ""), nil); err != nil {
		return nil, fmt.Errorf("loading built-in defaults: %w", err)
	}
	known := make(map[string]bool)
	for _, key := range k.Keys() {
		known[key] = true
	}

	l := &Loaded{k: k, Layers: []Layer{{Name: "defaults", Present: true}}}

	files := []struct {
		name, path string
		required   bool
	}{
		{"global", src.GlobalFile, false},
		{"project", src.ProjectFile, false},
		{"extra", src.ExtraFile, true},
	}
	for _, f := range files {
		present, err := loadFile(k, f.path, f.required)
		if err != nil {
			return nil, fmt.Errorf("%s config: %w", f.name, err)
		}
		if f.path != "" {
			l.Layers = append(l.Layers, Layer{Name: f.name, Path: f.path, Present: present})
		}
	}

	if src.Environ != nil {
		p := env.Provider(delim, env.Opt{
			Prefix:      EnvPrefix,
			EnvironFunc: func() []string { return src.Environ },
			TransformFunc: func(name, value string) (string, any) {
				key := envKey(name)
				// Unrelated RALPH_* variables, such as a token named by
				// pr.bitbucket_cloud.token_env, must never enter the config.
				if !known[key] {
					return "", nil
				}
				return key, value
			},
		})
		if err := k.Load(p, nil); err != nil {
			return nil, fmt.Errorf("environment config: %w", err)
		}
		l.Layers = append(l.Layers, Layer{Name: "environment", Present: envContributes(src.Environ, known)})
	}

	if src.Flags != nil {
		changed := false
		p := posflag.ProviderWithFlag(src.Flags, delim, k, func(f *pflag.Flag) (string, any) {
			key, ok := FlagKeys[f.Name]
			if !ok {
				return "", nil
			}
			if f.Changed {
				changed = true
			}
			return key, posflag.FlagVal(src.Flags, f)
		})
		if err := k.Load(p, nil); err != nil {
			return nil, fmt.Errorf("flag config: %w", err)
		}
		l.Layers = append(l.Layers, Layer{Name: "flags", Present: changed})
	}

	var cfg Config
	dc := &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
			mapstructure.TextUnmarshallerHookFunc(),
		),
		WeaklyTypedInput: true,
		ErrorUnused:      true,
	}
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "koanf", DecoderConfig: dc}); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config:\n%w", err)
	}
	l.Config = &cfg
	return l, nil
}

func loadFile(k *koanf.Koanf, path string, required bool) (bool, error) {
	if path == "" {
		return false, nil
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && !required:
		return false, nil
	case err != nil:
		return false, err
	case info.IsDir():
		return false, fmt.Errorf("%s is a directory", path)
	}
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

func envKey(name string) string {
	key := strings.ToLower(strings.TrimPrefix(name, EnvPrefix))
	return strings.ReplaceAll(key, strings.ToLower(EnvNestingSeparator), delim)
}

func envContributes(environ []string, known map[string]bool) bool {
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(name, EnvPrefix) && known[envKey(name)] {
			return true
		}
	}
	return false
}

// EnvVar returns the environment variable that overrides a config key.
func EnvVar(key string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, delim, EnvNestingSeparator))
}

// ProjectTemplate returns the built-in defaults with every setting commented
// out, suitable as a starting project config that overrides nothing.
func ProjectTemplate() []byte {
	var b strings.Builder
	b.WriteString("# ralph project configuration.\n")
	b.WriteString("# Layers, lowest to highest precedence: built-in defaults, global config,\n")
	b.WriteString("# this file, --config, RALPH_* environment variables, flags.\n")
	b.WriteString("# Uncomment a setting to override it for this project.\n\n")
	for _, line := range strings.SplitAfter(string(defaultsYAML), "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			b.WriteString(line)
		case strings.HasPrefix(strings.TrimSpace(line), "#"):
			b.WriteString(line)
		default:
			b.WriteString("# " + line)
		}
	}
	return []byte(b.String())
}
