package airplay

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is one AirPlay receiver: the bridge serves a single speaker.
type Config struct {
	APIURL      string
	AdminSecret string
	DeviceID    string
	Name        string // what the receiver is called in the AirPlay menus
	Port        int
	Interface   string // the node's network interface; empty = the first one
	// LatencyOffset moves the output from what the source asked for, in
	// seconds: the source delays its video by that, and the bridge and the
	// unit's prebuffer add their own after this point.
	LatencyOffset float64
}

func LoadConfig() (Config, error) {
	cfg := Config{
		APIURL:      strings.TrimRight(os.Getenv("SEBASTIAN_API_URL"), "/"),
		AdminSecret: os.Getenv("SEBASTIAN_ADMIN_SECRET"),
		DeviceID:    os.Getenv("SEBASTIAN_AIRPLAY_DEVICE_ID"),
		Name:        os.Getenv("SEBASTIAN_AIRPLAY_NAME"),
		Interface:   os.Getenv("SEBASTIAN_AIRPLAY_INTERFACE"),
		Port:        5000,
	}
	var errs []error
	for name, value := range map[string]string{
		"SEBASTIAN_API_URL":           cfg.APIURL,
		"SEBASTIAN_ADMIN_SECRET":      cfg.AdminSecret,
		"SEBASTIAN_AIRPLAY_DEVICE_ID": cfg.DeviceID,
		"SEBASTIAN_AIRPLAY_NAME":      cfg.Name,
	} {
		if value == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	if raw := os.Getenv("SEBASTIAN_AIRPLAY_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("SEBASTIAN_AIRPLAY_PORT: %w", err))
		}
		cfg.Port = port
	}
	if raw := os.Getenv("SEBASTIAN_AIRPLAY_LATENCY_OFFSET"); raw != "" {
		offset, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("SEBASTIAN_AIRPLAY_LATENCY_OFFSET: %w", err))
		}
		cfg.LatencyOffset = offset
	}
	return cfg, errors.Join(errs...)
}

// receiverConf renders the shairport-sync configuration. The UDP ports it
// allocates start right after Port, so receivers on one node need their Port
// at least 20 apart.
func receiverConf(cfg Config) string {
	var iface string
	if cfg.Interface != "" {
		iface = fmt.Sprintf("\tinterface = %s;\n", quoteConf(cfg.Interface))
	}
	return fmt.Sprintf(`general =
{
	name = %s;
	port = %d;
	udp_port_base = %d;
	output_backend = "stdout";
	audio_backend_latency_offset_in_seconds = %.3f;
%s};

sessioncontrol =
{
	allow_session_interruption = "yes";
};

stdout =
{
	output_rate = %d;
	output_format = "S16_LE";
	output_channels = %d;
};
`, quoteConf(cfg.Name), cfg.Port, cfg.Port+1, cfg.LatencyOffset, iface, sampleRate, channels)
}

func quoteConf(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}
