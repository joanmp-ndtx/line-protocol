package main

import (
	"fmt"
	"os"

	"github.com/joanmp-ndtx/line-protocol/v3/lineprotocol"
)

func main() {
	dec := lineprotocol.NewDecoder(os.Stdin)
	if !verify(dec) {
		os.Exit(1)
	}
}

func verify(dec *lineprotocol.Decoder) (ok bool) {
	logErr := func(err error) {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		ok = false
	}
nextLine:
	for dec.Next() {
		_, err := dec.Measurement()
		if err != nil {
			logErr(err)
			continue nextLine
		}
		for {
			key, _, err := dec.NextTag()
			if err != nil {
				logErr(err)
				continue nextLine
			}
			if key == nil {
				break
			}
		}
		for {
			key, _, err := dec.NextField()
			if err != nil {
				logErr(err)
				continue nextLine
			}
			if key == nil {
				break
			}
		}
		// TODO precision flag so we can check time bounds.
		if _, err := dec.TimeBytes(); err != nil {
			logErr(err)
			continue nextLine
		}
	}
	return ok
}
