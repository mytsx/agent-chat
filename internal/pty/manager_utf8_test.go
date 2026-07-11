package pty

import "testing"

func TestValidUTF8Len(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want int
	}{
		{
			name: "empty",
			in:   nil,
			want: 0,
		},
		{
			name: "ascii ends on boundary",
			in:   []byte("hello"),
			want: 5,
		},
		{
			name: "complete two byte rune",
			in:   []byte("aé"),
			want: len([]byte("aé")),
		},
		{
			name: "incomplete two byte rune",
			in:   append([]byte("a"), 0xc3),
			want: 1,
		},
		{
			name: "incomplete three byte rune",
			in:   append([]byte("a"), 0xe2, 0x82),
			want: 1,
		},
		{
			name: "incomplete four byte rune",
			in:   append([]byte("a"), 0xf0, 0x9f, 0x92),
			want: 1,
		},
		{
			name: "complete four byte rune",
			in:   []byte("a💬"),
			want: len([]byte("a💬")),
		},
		{
			name: "trailing continuation bytes are preserved by current scanner",
			in:   []byte{0x80, 0x80},
			want: 2,
		},
		{
			name: "lone invalid leading byte is treated as incomplete",
			in:   append([]byte("a"), 0xff),
			want: 1,
		},
		{
			name: "full width invalid leading sequence is kept as a boundary",
			in:   []byte{0xf5, 0x80, 0x80, 0x80},
			want: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validUTF8Len(tt.in); got != tt.want {
				t.Fatalf("validUTF8Len(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
