package teleport

import (
	"reflect"
	"testing"
)

func TestParseAllowedDBNames(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want []string
	}{
		{
			name: "single line, no color",
			msg:  "ERROR: please provide the database name using the --db-name flag, allowed database names for my-tunnel: [area ep-flowbird-sync operator-integration-config]",
			want: []string{"area", "ep-flowbird-sync", "operator-integration-config"},
		},
		{
			name: "wrapped across lines with ANSI color codes",
			msg: "\x1b[31mERROR:\x1b[0m please provide the database name using the --db-name\n" +
				"flag, allowed database names for production-operators-exp-rds-aurora-eu-west-1-783892415028:\n" +
				"[area, ep-flowbird-sync, operator-integration-config]",
			want: []string{"area", "ep-flowbird-sync", "operator-integration-config"},
		},
		{
			name: "quoted comma-separated",
			msg:  `allowed database names: ["area", "ep-flowbird-sync"]`,
			want: []string{"area", "ep-flowbird-sync"},
		},
		{
			name: "no brackets",
			msg:  "some unrelated error",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAllowedDBNames(tc.msg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseAllowedDBNames(%q) = %#v, want %#v", tc.msg, got, tc.want)
			}
		})
	}
}
