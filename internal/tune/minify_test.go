package tune

import "testing"

func TestMinifyJSComments(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"a = 1; // trailing\nb = 2;", "a=1;b=2;"},
		{"/* block\n comment */ x", "x"},
		{"s = 'has // not a comment';", `s='has // not a comment';`},
		{`s = "/* nor this */";`, `s="/* nor this */";`},
		{"t = 'escaped \\' still string // nope';", `t='escaped \' still string // nope';`},
	}
	for _, c := range cases {
		if got := minifyJS(c.in); got != c.want {
			t.Errorf("minifyJS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMinifyJSWhitespace(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"var  x  =  1 ;", "var x=1;"},
		{"function f ( a , b ) { return a + b ; }", "function f(a,b){return a+b;}"},
		{"if (a) b()", "if(a)b()"},
		{"else x", "else x"},                                 // two words must not fuse
		{"return -1", "return-1"},                            // word vs '-' may fuse (no such pattern relied on)
		{"a / b / c", "a/b/c"},                               // plain division keeps its spacing needs
		{"a = 1 /2", "a=1/2"},                                // division
		{`s = "  keep  spaces  ";`, `s="  keep  spaces  ";`}, // literal contents opaque
		{"  lead and trail  ", "lead and trail"},
	}
	for _, c := range cases {
		if got := minifyJS(c.in); got != c.want {
			t.Errorf("minifyJS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// needsSpace guards the two ways tokens may corrupt each other when glued:
// word+word fusing into one identifier, and / meeting a comment opener.
func TestMinifyJSNeedsSpace(t *testing.T) {
	yes := [][2]string{{"a", "b"}, {"1", "_x"}, {"$", "a"}, {"/", "/"}, {"*", "/"}, {"/", "*"}, {"/", "/"}}
	for _, p := range yes {
		if !needsSpace([]rune(p[0])[0], []rune(p[1])[0]) {
			t.Errorf("needsSpace(%q,%q) = false, want true", p[0], p[1])
		}
	}
	no := [][2]string{{"a", ")"}, {"(", "b"}, {";", "x"}, {"1", "+"}, {"}", "{"}}
	for _, p := range no {
		if needsSpace([]rune(p[0])[0], []rune(p[1])[0]) {
			t.Errorf("needsSpace(%q,%q) = true, want false", p[0], p[1])
		}
	}
}

func TestMinifyJSIdempotent(t *testing.T) {
	in := "var x = 1; // c\n/* b */ function f(){ return x ; }"
	once := minifyJS(in)
	if twice := minifyJS(once); twice != once {
		t.Errorf("not idempotent:\n 1st: %q\n 2nd: %q", once, twice)
	}
}
