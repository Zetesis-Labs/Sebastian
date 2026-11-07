package airplay

import (
	"strings"
	"testing"
)

func TestReceiverConf(t *testing.T) {
	conf := receiverConf(Config{Name: `Sala "grande"`, Port: 5020, LatencyOffset: 0.5})
	for _, want := range []string{
		`name = "Sala \"grande\"";`,
		"port = 5020;",
		"udp_port_base = 5021;",
		`output_backend = "stdout";`,
		"audio_backend_latency_offset_in_seconds = 0.500;",
		"output_rate = 48000;",
		`output_format = "S16_LE";`,
		"output_channels = 2;",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q in:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "interface") {
		t.Errorf("no interface was asked for:\n%s", conf)
	}
	if conf := receiverConf(Config{Name: "x", Interface: "eth0"}); !strings.Contains(conf, `interface = "eth0";`) {
		t.Errorf("missing the interface in:\n%s", conf)
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("SEBASTIAN_API_URL", "http://server:8787/")
	t.Setenv("SEBASTIAN_ADMIN_SECRET", "s")
	t.Setenv("SEBASTIAN_AIRPLAY_DEVICE_ID", "e072a1f96ef0")
	t.Setenv("SEBASTIAN_AIRPLAY_NAME", "Sebastian")
	t.Setenv("SEBASTIAN_AIRPLAY_PORT", "")
	t.Setenv("SEBASTIAN_AIRPLAY_LATENCY_OFFSET", "0.5")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIURL != "http://server:8787" || cfg.Port != 5000 || cfg.LatencyOffset != 0.5 {
		t.Fatalf("got %+v", cfg)
	}
}

func TestLoadConfigNamesWhatIsMissing(t *testing.T) {
	for _, name := range []string{"SEBASTIAN_API_URL", "SEBASTIAN_ADMIN_SECRET", "SEBASTIAN_AIRPLAY_DEVICE_ID", "SEBASTIAN_AIRPLAY_NAME"} {
		t.Setenv(name, "")
	}
	t.Setenv("SEBASTIAN_AIRPLAY_PORT", "nope")
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"SEBASTIAN_API_URL", "SEBASTIAN_ADMIN_SECRET", "SEBASTIAN_AIRPLAY_DEVICE_ID", "SEBASTIAN_AIRPLAY_NAME", "SEBASTIAN_AIRPLAY_PORT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not mention %s", err, want)
		}
	}
}
