package zfs

import "testing"

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{
		-1:            "-",
		0:             "0B",
		1023:          "1023B",
		1024:          "1.0KiB",
		1536:          "1.5KiB",
		1 << 20:       "1.0MiB",
		1 << 30:       "1.0GiB",
		3 * (1 << 40): "3.0TiB",
	}
	for input, want := range cases {
		if got := FormatBytes(input); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", input, got, want)
		}
	}
}
