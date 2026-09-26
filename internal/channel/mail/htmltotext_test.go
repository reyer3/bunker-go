package mail

import "testing"

func TestHTMLToText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips tags and collapses whitespace",
			in:   "<p>Hello   <b>world</b></p>",
			want: "Hello world",
		},
		{
			name: "block elements become line breaks",
			in:   "<div>Line one</div><div>Line two</div>",
			want: "Line one\nLine two",
		},
		{
			name: "br becomes a line break",
			in:   "Line one<br>Line two<br/>Line three",
			want: "Line one\nLine two\nLine three",
		},
		{
			name: "entities are decoded",
			in:   "Alice &amp; Claude &lt;bunker&gt;",
			want: "Alice & Claude <bunker>",
		},
		{
			name: "script and style content is dropped entirely",
			in:   "<style>.x{color:red}</style><p>Body</p><script>alert(1)</script>",
			want: "Body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTMLToText(tt.in); got != tt.want {
				t.Errorf("HTMLToText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
