package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// longFlagNames maps canonical config keys to explicit CLI long flag names.
var longFlagNames = map[string]string{
	"controllerUrl":          "controller-url",
	"controllerCert":         "controller-cert",
	"containerEngine":        "container-engine",
	"containerEngineUrl":     "container-engine-url",
	"networkInterface":       "network-interface",
	"diskLimitGiB":           "disk-limit-gib",
	"diskDirectory":          "disk-directory",
	"memoryLimitMiB":         "memory-limit-mib",
	"cpuLimitPercent":        "cpu-limit-percent",
	"logLimitGiB":            "log-limit-gib",
	"logDirectory":           "log-directory",
	"logFileCount":           "log-file-count",
	"logLevel":               "log-level",
	"statusFrequencySeconds": "status-frequency-seconds",
	"changeFrequencySeconds": "change-frequency-seconds",
	"watchdogEnabled":        "watchdog-enabled",
	"edgeGuardFrequency":     "edge-guard-frequency",
	"gpsMode":                "gps-mode",
	"gpsCoordinates":         "gps-coordinates",
	"gpsDevice":              "gps-device",
	"gpsScanFrequency":       "gps-scan-frequency",
	"arch":                   "arch",
	"secureMode":             "secure-mode",
	"pruningFrequency":       "pruning-frequency",
	"availableDiskThreshold": "available-disk-threshold",
	"upgradeScanFrequency":   "upgrade-scan-frequency",
	"devMode":                "dev-mode",
	"timezone":               "timezone",
}

func longFlagName(canonical string) string {
	if name, ok := longFlagNames[canonical]; ok {
		return name
	}
	return camelToKebab(canonical)
}

// FlagSet registers and collects config patch flags for the config command.
type FlagSet struct {
	bindings []*flagBinding
}

type flagBinding struct {
	canonical string
	rule      configKeyRule
	longName  string
	aliases   []string
	strVal    optionalString
	intVal    optionalInt
	floatVal  optionalFloat
	boolVal   optionalBool
}

type optionalString struct {
	set bool
	val string
}

func (o *optionalString) Set(s string) error {
	o.set = true
	o.val = s
	return nil
}

func (o *optionalString) String() string { return o.val }
func (o *optionalString) Type() string   { return "string" }

type optionalInt struct {
	set bool
	val int
}

func (o *optionalInt) Set(s string) error {
	o.set = true
	i, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	o.val = i
	return nil
}

func (o *optionalInt) String() string {
	if !o.set {
		return ""
	}
	return strconv.Itoa(o.val)
}
func (o *optionalInt) Type() string { return "int" }

type optionalFloat struct {
	set bool
	val float64
}

func (o *optionalFloat) Set(s string) error {
	o.set = true
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	o.val = f
	return nil
}

func (o *optionalFloat) String() string {
	if !o.set {
		return ""
	}
	return strconv.FormatFloat(o.val, 'f', -1, 64)
}
func (o *optionalFloat) Type() string { return "float" }

type optionalBool struct {
	set bool
	val bool
}

func (o *optionalBool) Set(s string) error {
	o.set = true
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "on", "yes":
		o.val = true
	case "false", "0", "off", "no":
		o.val = false
	default:
		return errors.New("must be one of true|false|on|off|1|0")
	}
	return nil
}

func (o *optionalBool) String() string {
	if !o.set {
		return ""
	}
	return strconv.FormatBool(o.val)
}
func (o *optionalBool) Type() string { return "bool" }

// NewFlagSet builds flag bindings from configKeyRules.
func NewFlagSet() *FlagSet {
	keys := sortedConfigRuleKeys()
	bindings := make([]*flagBinding, 0, len(keys))
	for _, canonical := range keys {
		rule := configKeyRules[canonical]
		bindings = append(bindings, &flagBinding{
			canonical: canonical,
			rule:      rule,
			longName:  longFlagName(canonical),
			aliases:   shortFlagNames(canonical, rule.Aliases),
		})
	}
	return &FlagSet{bindings: bindings}
}

// Register adds long and short alias flags to cmd.
func (fs *FlagSet) Register(cmd *cobra.Command) {
	if cmd == nil || fs == nil {
		return
	}
	for _, binding := range fs.bindings {
		binding.register(cmd.Flags())
	}
}

func (b *flagBinding) register(flags *pflag.FlagSet) {
	help := flagUsageLine(b.rule)
	if aliasText := formatAliasFlags(b.aliases); aliasText != "" {
		help += ". Alias: " + aliasText
	}
	var mainFlag *pflag.Flag
	switch b.rule.Type {
	case configValueInt:
		flags.Var(&b.intVal, b.longName, help)
		mainFlag = flags.Lookup(b.longName)
	case configValueFloat:
		flags.Var(&b.floatVal, b.longName, help)
		mainFlag = flags.Lookup(b.longName)
	case configValueBool:
		flags.Var(&b.boolVal, b.longName, help)
		mainFlag = flags.Lookup(b.longName)
	default:
		flags.Var(&b.strVal, b.longName, help)
		mainFlag = flags.Lookup(b.longName)
	}
	for _, alias := range b.aliases {
		flags.AddFlag(&pflag.Flag{
			Name:     alias,
			Value:    mainFlag.Value,
			Usage:    help,
			DefValue: mainFlag.DefValue,
			Hidden:   true,
		})
	}
}

// Collect reads changed flags into a validated set map.
func (fs *FlagSet) Collect(flags *pflag.FlagSet) (map[string]any, error) {
	if fs == nil {
		return nil, errors.New("config flags are unavailable")
	}
	setMap := make(map[string]any)
	for _, binding := range fs.bindings {
		if !binding.isChanged(flags) {
			continue
		}
		raw, err := binding.rawValue()
		if err != nil {
			return nil, err
		}
		normalized, err := validateAndNormalizeConfigValue(binding.rule, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", binding.canonical, err)
		}
		setMap[binding.canonical] = normalized
	}
	if len(setMap) == 0 {
		return nil, errors.New("at least one config flag is required (see edgelet config --help)")
	}
	return setMap, nil
}

func (b *flagBinding) isChanged(flags *pflag.FlagSet) bool {
	if flags == nil {
		return false
	}
	if f := flags.Lookup(b.longName); f != nil && f.Changed {
		return true
	}
	for _, alias := range b.aliases {
		if f := flags.Lookup(alias); f != nil && f.Changed {
			return true
		}
	}
	return false
}

func (b *flagBinding) rawValue() (string, error) {
	switch b.rule.Type {
	case configValueInt:
		if !b.intVal.set {
			return "", fmt.Errorf("flag --%s was not set", b.longName)
		}
		return strconv.Itoa(b.intVal.val), nil
	case configValueFloat:
		if !b.floatVal.set {
			return "", fmt.Errorf("flag --%s was not set", b.longName)
		}
		return strconv.FormatFloat(b.floatVal.val, 'f', -1, 64), nil
	case configValueBool:
		if !b.boolVal.set {
			return "", fmt.Errorf("flag --%s was not set", b.longName)
		}
		return strconv.FormatBool(b.boolVal.val), nil
	default:
		if !b.strVal.set {
			return "", fmt.Errorf("flag --%s was not set", b.longName)
		}
		return b.strVal.val, nil
	}
}

func camelToKebab(name string) string {
	if name == "" {
		return name
	}
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			_ = b.WriteByte('-')
		}
		_, _ = b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

func shortFlagNames(canonical string, aliases []string) []string {
	longName := longFlagName(canonical)
	seen := map[string]struct{}{longName: {}, canonical: {}}
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		name := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(alias), "--"), "-")
		if name == "" {
			continue
		}
		kebab := camelToKebab(name)
		if _, ok := seen[name]; ok {
			continue
		}
		if _, ok := seen[kebab]; ok {
			continue
		}
		if strings.EqualFold(name, canonical) || strings.EqualFold(kebab, longName) {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func flagUsageLine(rule configKeyRule) string {
	line := rule.Help
	if len(rule.Enums) > 0 {
		line += " (" + strings.Join(rule.Enums, "|") + ")"
	}
	return line
}

func formatAliasFlags(aliases []string) string {
	if len(aliases) == 0 {
		return ""
	}
	parts := make([]string, len(aliases))
	for i, alias := range aliases {
		parts[i] = "--" + alias
	}
	return strings.Join(parts, ", ")
}
