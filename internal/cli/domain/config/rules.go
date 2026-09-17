package config

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

type configValueType string

const (
	configValueString configValueType = "string"
	configValueInt    configValueType = "int"
	configValueFloat  configValueType = "float"
	configValueBool   configValueType = "bool"
)

type configKeyRule struct {
	Key     string
	Aliases []string
	Type    configValueType
	Enums   []string
	Min     *float64
	Max     *float64
	Help    string
}

var configKeyRules = map[string]configKeyRule{
	"controllerUrl":          {Key: "controllerUrl", Aliases: []string{"a"}, Type: configValueString, Help: "controller URL"},
	"controllerCert":         {Key: "controllerCert", Aliases: []string{"ac"}, Type: configValueString, Help: "controller CA certificate file path"},
	"containerEngine":        {Key: "containerEngine", Aliases: []string{"ce"}, Type: configValueString, Enums: []string{"docker", "podman", "edgelet"}, Help: "container engine"},
	"containerEngineUrl":     {Key: "containerEngineUrl", Aliases: []string{"cu"}, Type: configValueString, Help: "runtime socket URL"},
	"networkInterface":       {Key: "networkInterface", Aliases: []string{"n"}, Type: configValueString, Help: "network interface"},
	"diskLimitGiB":           {Key: "diskLimitGiB", Aliases: []string{"d"}, Type: configValueFloat, Help: "disk usage limit (GiB)"},
	"diskDirectory":          {Key: "diskDirectory", Aliases: []string{"dl"}, Type: configValueString, Help: "disk directory"},
	"memoryLimitMiB":         {Key: "memoryLimitMiB", Aliases: []string{"m"}, Type: configValueFloat, Help: "memory limit (MiB)"},
	"cpuLimitPercent":        {Key: "cpuLimitPercent", Aliases: []string{"p"}, Type: configValueFloat, Help: "CPU limit (%)"},
	"logLimitGiB":            {Key: "logLimitGiB", Aliases: []string{"l"}, Type: configValueFloat, Help: "log limit (GiB)"},
	"logDirectory":           {Key: "logDirectory", Aliases: []string{"ld"}, Type: configValueString, Help: "log directory"},
	"logFileCount":           {Key: "logFileCount", Aliases: []string{"lc"}, Type: configValueInt, Help: "log file count"},
	"logLevel":               {Key: "logLevel", Aliases: []string{"ll"}, Type: configValueString, Enums: []string{"DEBUG", "INFO", "WARN", "ERROR"}, Help: "log level"},
	"statusFrequencySeconds": {Key: "statusFrequencySeconds", Aliases: []string{"sf"}, Type: configValueInt, Help: "status frequency (seconds)"},
	"changeFrequencySeconds": {Key: "changeFrequencySeconds", Aliases: []string{"cf"}, Type: configValueInt, Help: "change polling frequency (seconds)"},
	"watchdogEnabled":        {Key: "watchdogEnabled", Aliases: []string{"wd"}, Type: configValueBool, Help: "watchdog enable"},
	"edgeGuardFrequency":     {Key: "edgeGuardFrequency", Aliases: []string{"egf"}, Type: configValueInt, Help: "edge guard frequency"},
	"gpsMode":                {Key: "gpsMode", Aliases: []string{"gps"}, Type: configValueString, Enums: []string{"auto", "dynamic", "manual", "off"}, Help: "GPS mode"},
	"gpsCoordinates":         {Key: "gpsCoordinates", Aliases: []string{"gpsc"}, Type: configValueString, Help: "GPS coordinates lat,lon"},
	"gpsDevice":              {Key: "gpsDevice", Aliases: []string{"gpsd"}, Type: configValueString, Help: "GPS device"},
	"gpsScanFrequency":       {Key: "gpsScanFrequency", Aliases: []string{"gpsf"}, Type: configValueInt, Help: "GPS scan frequency"},
	"arch":                   {Key: "arch", Aliases: []string{"ft"}, Type: configValueString, Enums: []string{"auto", "amd64", "arm64", "arm", "riscv64"}, Help: "fog type/arch"},
	"secureMode":             {Key: "secureMode", Aliases: []string{"sec"}, Type: configValueBool, Help: "secure mode"},
	"pruningFrequency":       {Key: "pruningFrequency", Aliases: []string{"pf"}, Type: configValueInt, Help: "prune frequency"},
	"availableDiskThreshold": {Key: "availableDiskThreshold", Aliases: []string{"dt"}, Type: configValueInt, Help: "available disk threshold"},
	"upgradeScanFrequency":   {Key: "upgradeScanFrequency", Aliases: []string{"uf"}, Type: configValueInt, Help: "upgrade scan frequency"},
	"devMode":                {Key: "devMode", Aliases: []string{"dev"}, Type: configValueBool, Help: "developer mode"},
	"timezone":               {Key: "timezone", Aliases: []string{"tz"}, Type: configValueString, Help: "timezone"},
}

func validateAndNormalizeConfigValue(rule configKeyRule, value string) (any, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("value cannot be empty")
	}
	if rule.Key == "controllerCert" {
		data, err := os.ReadFile(value) // #nosec G304 -- user-supplied path intentionally validated for local cert file
		if err != nil {
			return nil, errors.New("must be a readable PEM certificate file path")
		}
		block, _ := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, errors.New("must be a readable PEM certificate file path")
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return nil, errors.New("must be a readable PEM certificate file path")
		}
		return value, nil
	}
	switch rule.Type {
	case configValueInt:
		i, err := strconv.Atoi(value)
		if err != nil {
			return nil, errors.New("must be an integer")
		}
		return i, nil
	case configValueFloat:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, errors.New("must be a number")
		}
		return f, nil
	case configValueBool:
		switch strings.ToLower(value) {
		case "true", "1", "on", "yes":
			return true, nil
		case "false", "0", "off", "no":
			return false, nil
		default:
			return nil, errors.New("must be one of true|false|on|off|1|0")
		}
	default:
		if len(rule.Enums) > 0 {
			for _, allowed := range rule.Enums {
				if strings.EqualFold(allowed, value) {
					return value, nil
				}
			}
			return nil, fmt.Errorf("must be one of %s", strings.Join(rule.Enums, "|"))
		}
		return value, nil
	}
}

func sortedConfigRuleKeys() []string {
	keys := make([]string, 0, len(configKeyRules))
	for key := range configKeyRules {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
