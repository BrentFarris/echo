package ctest

import "testing"

func TestDiscoverFunctionDefinitionOnly(t *testing.T) {
	source := `
#define main(x) ignored(x)
// int main(void) { return 1; }
const char *text = "int main(void) { return 2; }";
int main(void);
int call(void) { return main(); }

static int
main(
    void
)
{
    return 0;
}
`
	rangeValue, ok := discoverFunction(source, "main")
	if !ok {
		t.Fatal("function definition was not found")
	}
	if rangeValue.Start.Line != 8 || rangeValue.Start.Character != 0 {
		t.Fatalf("range = %#v", rangeValue)
	}
}

func TestDiscoverFunctionRejectsPrototypeCallAndMalformedSource(t *testing.T) {
	for _, source := range []string{
		"int tests(void);\n",
		"int wrapper(void) { return tests(); }\n",
		"int (*tests)(void);\n",
		"int tests(void)\n",
		"int tests(void)\nstruct other { int value; };\n",
		"#define TEST_ENTRY int tests(void) { return 0; }\n",
		"#define TEST_ENTRY \\\r\n int tests(void) {}\r\n",
	} {
		if _, ok := discoverFunction(source, "tests"); ok {
			t.Fatalf("unexpected definition in %q", source)
		}
	}
}

func TestDiscoverFunctionUsesUTF16Columns(t *testing.T) {
	rangeValue, ok := discoverFunction("/* 😀 */ int tests(void) {}\n", "tests")
	if !ok || rangeValue.Start.Character != 13 {
		t.Fatalf("range = %#v, found = %v", rangeValue, ok)
	}
}

func TestDiscoverFunctionAllowsPostfixAttributes(t *testing.T) {
	if _, ok := discoverFunction("int tests(void) __attribute__((const)) {}\n", "tests"); !ok {
		t.Fatal("postfix attribute hid a function definition")
	}
}
