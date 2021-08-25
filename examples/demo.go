package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/midbel/sax"
)

const sample = `
<?xml version="1.0" encoding="UTF-8"?>
<!-- this is a -- comment -->
<root xmlns:sax="http://localhost">
  <sax:DocumentElement sax:param="value">
    <First-Element>
      &#x21; Some Text
    </First-Element>
    <?some_pi some_attr="some_value"?>
    <SecondElement param2="something">
      Pre-Text <Inline>Inlined text</Inline> Post-text.
    </SecondElement>
    <script>
      <![CDATA[
        <message>Welcome</message>
      ]]>
    </script>
  </sax:DocumentElement>
</root>
`

func main() {
	flag.Parse()

	r, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer r.Close()

	rs := sax.New(r, func(t sax.NodeType, _ sax.Name) bool {
		return t == sax.BeginElement || t == sax.EndElement
	})
	// rs := New(strings.NewReader(sample), nil)
	for i := 0; ; i++ {
		n, err := rs.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintln(os.Stderr, err)
			}
			break
		}
		fmt.Printf("%d (%d): %s %v %s (%s)\n", i+1, rs.Depth(), n.Name, n.Attrs, n.Content, n.Type)
	}
}
