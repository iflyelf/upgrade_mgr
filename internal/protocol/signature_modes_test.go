package protocol

import (
	"reflect"
	"testing"
)

func TestNormalizeSignatureModes(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "去重并按稳定顺序",
			input: []string{" V2 ", "id", "signed_path", "id"},
			want:  []string{SignatureModeID, SignatureModeSignedPath, SignatureModeV2},
		},
		{
			name:  "忽略未知能力",
			input: []string{"future_mode", "v2", "unknown"},
			want:  []string{SignatureModeV2},
		},
		{
			name:  "空输入",
			input: nil,
			want:  []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeSignatureModes(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NormalizeSignatureModes(%v) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPreferredLegacySignatureMode(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  string
	}{
		{name: "旧 Agent 未上报默认 id", input: nil, want: SignatureModeID},
		{name: "仅支持 id", input: []string{SignatureModeID}, want: SignatureModeID},
		{name: "支持 signed_path", input: []string{SignatureModeSignedPath}, want: SignatureModeSignedPath},
		{name: "同时支持时优先 signed_path", input: []string{SignatureModeID, SignatureModeSignedPath, SignatureModeV2}, want: SignatureModeSignedPath},
		{name: "仅支持 v2 时 legacy 回退 id", input: []string{SignatureModeV2}, want: SignatureModeID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PreferredLegacySignatureMode(tc.input); got != tc.want {
				t.Fatalf("PreferredLegacySignatureMode(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
