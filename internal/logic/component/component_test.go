package component

import "testing"

func TestValidateScriptURL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "empty", raw: "", wantErr: false},
		{name: "http", raw: "http://repo.example.com/install.sh", wantErr: false},
		{name: "https with query", raw: "https://repo.example.com/install.sh?token=abc", wantErr: false},
		{name: "real node_exporter example", raw: "https://proxy.xiaonuo.live/?url=https://raw.githubusercontent.com/iflyelf/upgrade_mgr/main/examples/node_exporter/install.sh", wantErr: false},
		{name: "uppercase scheme", raw: "HTTPS://repo.example.com/install.sh", wantErr: false},
		{name: "relative", raw: "/install.sh", wantErr: true},
		{name: "file scheme", raw: "file:///etc/passwd", wantErr: true},
		{name: "ftp scheme", raw: "ftp://repo.example.com/install.sh", wantErr: true},
		{name: "missing host", raw: "https:///install.sh", wantErr: true},
		{name: "invalid host", raw: "http://exa mple.com/install.sh", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateScriptURL(tc.raw); (err != nil) != tc.wantErr {
				t.Fatalf("ValidateScriptURL(%q) err=%v, wantErr=%v", tc.raw, err, tc.wantErr)
			}
		})
	}
}
