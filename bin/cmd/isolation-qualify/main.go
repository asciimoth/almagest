// Command isolation-qualify validates development-only complete-profile evidence.
// It does not enable isolated daemon startup.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/asciimoth/almagest/bin/internal/isolation/qualification"
)

func main() {
	evidencePath := flag.String("evidence", "", "complete-profile evidence JSON file")
	list := flag.Bool("list-profiles", false, "list known qualification profiles")
	flag.Parse()
	if *list {
		if *evidencePath != "" || flag.NArg() != 0 {
			fail("-list-profiles does not accept other arguments")
		}
		for _, profile := range qualification.Profiles() {
			fmt.Println(profile)
		}
		return
	}
	if *evidencePath == "" || flag.NArg() != 0 {
		fail("usage: isolation-qualify -evidence EVIDENCE")
	}
	data, err := os.ReadFile(*evidencePath)
	if err != nil {
		fail("read evidence: %v", err)
	}
	evidence, err := qualification.Decode(data)
	if err == nil {
		err = qualification.Validate(evidence)
	}
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("Isolation profile evidence passed for %s\n", evidence.Profile)
}

func fail(format string, values ...any) {
	fmt.Fprintf(os.Stderr, "isolation-qualify: "+format+"\n", values...)
	os.Exit(1)
}
