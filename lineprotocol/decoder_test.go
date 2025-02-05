package lineprotocol

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
	"time"
	"unicode/utf8"

	qt "github.com/frankban/quicktest"
)

type TagKeyValue struct {
	Key, Value string
	Error      string
}

type FieldKeyValue struct {
	Key   string
	Value interface{}
	Error string
}

type Point struct {
	Measurement      string
	MeasurementError string
	Tags             []TagKeyValue
	Fields           []FieldKeyValue
	Precision        Precision
	DefaultTime      time.Time
	Time             time.Time
	TimeError        string
}

func isDecodeError(err error) bool {
	return errors.As(err, new(*DecodeError))
}

// sectionCheckers holds a function for each section that checks that the result of decoding
// for that section is as expected.
var sectionCheckers = []func(c *qt.C, dec *Decoder, expect Point, errp errPositions){
	measurementSection: func(c *qt.C, dec *Decoder, expect Point, errp errPositions) {
		m, err := dec.Measurement()
		if expect.MeasurementError != "" {
			c.Assert(err, qt.Satisfies, isDecodeError)
			c.Assert(err, qt.ErrorMatches, regexp.QuoteMeta(errp.makeErr(expect.MeasurementError)), qt.Commentf("measurement %q", m))
			return
		}

		c.Assert(err, qt.IsNil)
		c.Assert(string(m), qt.Equals, expect.Measurement, qt.Commentf("runes: %x", []rune(string(m))))
	},
	tagSection: func(c *qt.C, dec *Decoder, expect Point, errp errPositions) {
		var tags []TagKeyValue
		for {
			key, value, err := dec.NextTag()
			if err != nil {
				c.Assert(key, qt.IsNil)
				c.Assert(value, qt.IsNil)
				c.Assert(err, qt.Satisfies, isDecodeError)
				tags = append(tags, TagKeyValue{
					Error: err.Error(),
				})
				continue
			}
			if key == nil {
				break
			}
			tags = append(tags, TagKeyValue{
				Key:   string(key),
				Value: string(value),
			})
		}
		// Translate the positions in the expected errors.
		expectTags := append([]TagKeyValue(nil), expect.Tags...)
		for i := range expectTags {
			tag := &expectTags[i]
			tag.Error = errp.makeErr(tag.Error)
		}
		c.Assert(tags, qt.DeepEquals, expectTags)
	},
	fieldSection: func(c *qt.C, dec *Decoder, expect Point, errp errPositions) {
		var fields []FieldKeyValue
		for {
			key, value, err := dec.NextField()
			if err != nil {
				if s := err.Error(); strings.Contains(s, "out of range") {
					if !errors.Is(err, ErrValueOutOfRange) {
						c.Errorf("out of range error not propagated to result error")
					}
				}
				c.Assert(err, qt.Satisfies, isDecodeError)
				c.Assert(key, qt.IsNil)
				fields = append(fields, FieldKeyValue{
					Error: err.Error(),
				})
				continue
			}
			if key == nil {
				break
			}
			fields = append(fields, FieldKeyValue{
				Key:   string(key),
				Value: value.Interface(),
			})
		}
		// Translate the positions in the expected errors.
		expectFields := append([]FieldKeyValue(nil), expect.Fields...)
		for i := range expectFields {
			field := &expectFields[i]
			field.Error = errp.makeErr(field.Error)
		}
		c.Assert(fields, qt.DeepEquals, expectFields)
	},
	timeSection: func(c *qt.C, dec *Decoder, expect Point, errp errPositions) {
		timestamp, err := dec.Time(expect.Precision, expect.DefaultTime)
		if expect.TimeError != "" {
			c.Assert(err, qt.Satisfies, isDecodeError)
			c.Assert(err, qt.ErrorMatches, regexp.QuoteMeta(errp.makeErr(expect.TimeError)))
			c.Assert(timestamp, qt.DeepEquals, time.Time{})
			return
		}
		c.Assert(err, qt.IsNil)
		c.Assert(timestamp, qt.DeepEquals, expect.Time)
	},
}

var decoderTests = []struct {
	testName string
	// text holds the text to be decoded.
	// the position of an error is marked by a ∑ character (the error
	// string contains a corresponding ∑ character, signifying that
	// it's expected to be a DecodeError at that error position.
	text   string
	expect []Point
}{
	{
		testName: "all-fields-present-no-escapes",
		text: `
   # comment
 somename,tag1=val1,tag2=val2  floatfield=1,strfield="hello",intfield=-1i,uintfield=1u,boolfield=true  1602841605822791506
`,
		expect: []Point{{
			Measurement: "somename",
			Tags: []TagKeyValue{{
				Key:   "tag1",
				Value: "val1",
			}, {
				Key:   "tag2",
				Value: "val2",
			}},
			Fields: []FieldKeyValue{{
				Key:   "floatfield",
				Value: 1.0,
			}, {
				Key:   "strfield",
				Value: "hello",
			}, {
				Key:   "intfield",
				Value: int64(-1),
			}, {
				Key:   "uintfield",
				Value: uint64(1),
			}, {
				Key:   "boolfield",
				Value: true,
			}},
			Time: time.Unix(0, 1602841605822791506),
		}},
	}, {
		testName: "multiple-entries",
		text: `
   # comment
 m1,tag1=val1  x="first"  1602841605822791506
  m2,foo=bar  x="second"  1602841605822792000

 # last comment
`,
		expect: []Point{{
			Measurement: "m1",
			Tags: []TagKeyValue{{
				Key:   "tag1",
				Value: "val1",
			}},
			Fields: []FieldKeyValue{{
				Key:   "x",
				Value: "first",
			}},
			Time: time.Unix(0, 1602841605822791506),
		}, {
			Measurement: "m2",
			Tags: []TagKeyValue{{
				Key:   "foo",
				Value: "bar",
			}},
			Fields: []FieldKeyValue{{
				Key:   "x",
				Value: "second",
			}},
			Time: time.Unix(0, 1602841605822792000),
		}},
	}, {
		testName: "multiple-entries-with-error#1",
		text: `
m1 value=12.0
m2 value=∑¹2a.0
m3 value=32.0
m4 value=42.0
`,
		expect: []Point{{
			Measurement: "m1",
			Fields: []FieldKeyValue{{
				Key:   "value",
				Value: 12.0,
			}},
		}, {
			Measurement: "m2",
			Fields: []FieldKeyValue{{
				Error: `at line ∑¹: cannot parse value for field key "value": invalid float value syntax`,
			}},
		}, {
			Measurement: "m3",
			Fields: []FieldKeyValue{{
				Key:   "value",
				Value: 32.0,
			}},
		}, {
			Measurement: "m4",
			Fields: []FieldKeyValue{{
				Key:   "value",
				Value: 42.0,
			}},
		}},
	}, {
		testName: "multiple-entries-with-error#2",
		text: `
m1 value=12.0
m2∑¹
m3 value=32.0
`,
		expect: []Point{{
			Measurement: "m1",
			Fields: []FieldKeyValue{{
				Key:   "value",
				Value: 12.0,
			}},
		}, {
			Measurement: "m2",
			// DecoderSkipSection: expect to see no error on TagSection but got error
			Tags: []TagKeyValue{{
				Error: `at line ∑¹: expected tag key or field but found '\n' instead`,
			}},
		}, {
			Measurement: "m3",
			Fields: []FieldKeyValue{{
				Key:   "value",
				Value: 32.0,
			}},
		}},
	}, {
		testName: "escaped-values",
		text: `
 comma\,1,equals\==e\,x,two=val2 field\=x="fir\"
,st\\"  1602841605822791506

 # last comment
`,
		expect: []Point{{
			Measurement: "comma,1",
			Tags: []TagKeyValue{{
				Key:   "equals=",
				Value: "e,x",
			}, {
				Key:   "two",
				Value: "val2",
			}},
			Fields: []FieldKeyValue{{
				Key:   "field=x",
				Value: "fir\"\n,st\\",
			}},
			Time: time.Unix(0, 1602841605822791506),
		}},
	}, {
		testName: "missing-quotes",
		text:     `TestBucket FieldOné=∑¹Happy,FieldTwo=sad`,
		expect: []Point{{
			Measurement: "TestBucket",
			Fields: []FieldKeyValue{{
				Error: `at line ∑¹: value for field "FieldOné" ("Happy") has unrecognized type`,
			}},
		}},
	}, {
		testName: "trailing-comma-after-measurement",
		text: `TestBuckét,∑¹ FieldOne=Happy
next x=1`,
		expect: []Point{{
			MeasurementError: "at line ∑¹: expected tag key after comma; got white space instead",
		}, {
			Measurement: "next",
			Fields: []FieldKeyValue{{
				Key:   "x",
				Value: 1.0,
			}},
		}},
	}, {
		testName: "missing-comma-after-field",
		text:     `TestBuckét TagOné="Happy" ∑¹FieldOne=123.45`,
		expect: []Point{{
			Measurement: "TestBuckét",
			Fields: []FieldKeyValue{{
				Key:   "TagOné",
				Value: "Happy",
			}},
			TimeError: `at line ∑¹: invalid timestamp ("FieldOne=123.45")`,
		}},
	}, {
		testName: "missing timestamp",
		text:     "b f=1",
		expect: []Point{{
			Measurement: "b",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: 1.0,
			}},
		}},
	}, {
		testName: "missing timestamp with newline",
		text:     "b f=1\n",
		expect: []Point{{
			Measurement: "b",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: 1.0,
			}},
		}},
	}, {
		testName: "out-of-range-timestamp",
		text:     "b f=1 ∑¹9223372036854775808",
		expect: []Point{{
			Measurement: "b",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: 1.0,
			}},
			TimeError: `at line ∑¹: invalid timestamp ("9223372036854775808"): line-protocol value out of range`,
		}},
	}, {
		testName: "out-of-range-timestamp-due-to-precision",
		text:     "b f=1 ∑¹200000000000000000",
		expect: []Point{{
			Measurement: "b",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: 1.0,
			}},
			Precision: Second,
			TimeError: `at line ∑¹: invalid timestamp ("200000000000000000"): line-protocol value out of range`,
		}},
	}, {
		testName: "negative-timestamp-just-in-range",
		text:     "b f=1 ∑¹-9223372036854775808",
		expect: []Point{{
			Measurement: "b",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: 1.0,
			}},
			Time: time.Unix(0, -9223372036854775808),
		}},
	}, {
		testName: "negative-timestamp-just-out-of-range",
		text:     "b f=1 ∑¹-9223372036854775809",
		expect: []Point{{
			Measurement: "b",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: 1.0,
			}},
			TimeError: `at line ∑¹: invalid timestamp ("-9223372036854775809"): line-protocol value out of range`,
		}},
	}, {
		testName: "missing-timestamp-with-default",
		text:     "b f=1",
		expect: []Point{{
			Measurement: "b",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: 1.0,
			}},
			DefaultTime: time.Unix(0, 1643971097811314803),
			Time:        time.Unix(0, 1643971097811314803),
		}},
	}, {
		testName: "field-with-space-and-no-timestamp",
		text:     "9 f=-7 ",
		expect: []Point{{
			Measurement: "9",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: -7.0,
			}},
		}},
	}, {
		testName: "carriage-returns",
		text:     "# foo\r\nm x=1\r\n\r\n",
		expect: []Point{{
			Measurement: "m",
			Fields: []FieldKeyValue{{
				Key:   "x",
				Value: 1.0,
			}},
		}},
	}, {
		testName: "carriage-return-in-comment",
		text:     "∑¹# foo\rxxx\nm x=1\r\n\r\n",
		expect: []Point{{
			MeasurementError: "at line ∑¹: invalid character found in comment line",
		}, {
			Measurement: "m",
			Fields: []FieldKeyValue{{
				Key:   "x",
				Value: 1.0,
			}},
		}},
	}, {
		// This test ensures that the ErrValueOutOfRange error is
		// propagated correctly with errors.Is
		testName: "out-of-range-value",
		text:     "mmmé é=∑¹1e9999999999999",
		expect: []Point{{
			Measurement: "mmmé",
			Fields: []FieldKeyValue{{
				Error: `at line ∑¹: cannot parse value for field key "é": line-protocol value out of range`,
			}},
		}},
	}, {
		testName: "field-key-error-after-newline-in-string",
		// Note: we've deliberately got two fields below so that
		// if we ever change error behaviour so that the caller
		// can see multiple errors on a single line, this test should
		// fail (see comment in the Next method).
		text: "m f=1,∑¹\x01=1,\x01=2",
		expect: []Point{{
			Measurement: "m",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: 1.0,
			}, {
				Error: `at line ∑¹: invalid character '\x01' found at start of field key`,
			}},
		}},
	}, {
		testName: "field-value-error-after-newline-in-string",
		text:     "m f=\"hello\ngoodbye\nx\",gé=∑¹invalid",
		expect: []Point{{
			Measurement: "m",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: "hello\ngoodbye\nx",
			}, {
				Error: `at line ∑¹: value for field "gé" ("invalid") has unrecognized type`,
			}},
		}},
	}, {
		testName: "field-string-value-error-after-newline-in-string",
		text:     "m f=\"a\nb\",g=∑¹\"c\nd",
		expect: []Point{{
			Measurement: "m",
			Fields: []FieldKeyValue{{
				Key:   "f",
				Value: "a\nb",
			}, {
				Error: `at line ∑¹: expected closing quote for string field "g", found end of input`,
			}},
		}},
	}, {
		testName: "non-printable-ASCII-in-tag-key",
		text:     "m foo∑¹\x01=bar x=1",
		expect: []Point{{
			Measurement: "m",
			Fields: []FieldKeyValue{{
				Error: `at line ∑¹: want '=' after field key "foo", found '\x01'`,
			}},
		}},
	}, {
		testName: "non-printable-ASCII-in-tag-key",
		text:     "m,∑¹foo\x03=bar x=1",
		expect: []Point{{
			Measurement: "m",
			Tags: []TagKeyValue{{
				Error: `at line ∑¹: expected '=' after tag key "foo", but got '\x03' instead`,
			}},
		}},
	}, {
		testName: "non-printable-ASCII-in-tag-value",
		text:     "m,foo=bar∑¹\x02 x=1",
		expect: []Point{{
			Measurement: "m",
			Tags: []TagKeyValue{{
				Key:   "foo",
				Value: "bar",
			}, {
				Error: `at line ∑¹: expected tag key or field but found '\x02' instead`,
			}},
		}},
	}, {
		testName: "non-printable-ASCII-in-field-key",
		text:     "m foo∑¹\x01=bar",
		expect: []Point{{
			Measurement: "m",
			Fields: []FieldKeyValue{{
				Error: `at line ∑¹: want '=' after field key "foo", found '\x01'`,
			}},
		}},
	}, {
		testName: "backslash-escapes-in-string-field",
		text:     `m s="\t\r\n\v"`,
		expect: []Point{{
			Measurement: "m",
			Fields: []FieldKeyValue{{
				Key:   "s",
				Value: "\t\r\n\\v",
			}},
		}},
	}, {
		testName: "backslash-escapes-in-tags",
		text:     `m,s=\t\r\n\v x=1`,
		expect: []Point{{
			Measurement: "m",
			Tags: []TagKeyValue{{
				Key:   "s",
				Value: `\t\r\n\v`,
			}},
			Fields: []FieldKeyValue{{
				Key:   "x",
				Value: 1.0,
			}},
		}},
	}, {
		testName: "bad-tag-key-#1",
		text:     "m∑¹",
		expect: []Point{{
			Measurement: "m",
			Tags: []TagKeyValue{{
				Error: `at line ∑¹: expected tag key or field but found end of input instead`,
			}},
		}},
	}, {
		testName: "bad-tag-key-#2",
		text:     "m,∑¹=bar¹",
		expect: []Point{{
			Measurement: "m",
			Tags: []TagKeyValue{{
				Error: `at line ∑¹: empty tag key`,
			}},
		}},
	}, {
		testName: "bad-tag-key-#3",
		text:     "m,∑¹x =y¹",
		expect: []Point{{
			Measurement: "m",
			Tags: []TagKeyValue{{
				Error: `at line ∑¹: expected '=' after tag key "x", but got ' ' instead`,
			}},
		}},
	}, {
		testName: "bad-tag-key-#4",
		text:     "m,∑¹x",
		expect: []Point{{
			Measurement: "m",
			Tags: []TagKeyValue{{
				Error: `at line ∑¹: expected '=' after tag key "x", but got end of input instead`,
			}},
		}},
	}, {
		testName: "tags-without-value",
		text:     `my-mesurement,tag1=va1,tag2=val2,tag3=val3,tag4=,tag5=val5 my-field-1=275,my-field-2=2048 1720618024249000000`,
		expect: []Point{{
			Measurement: "my-mesurement",
			Tags: []TagKeyValue{{
				Key:   "tag1",
				Value: "va1",
			}, {
				Key:   "tag2",
				Value: "val2",
			}, {
				Key:   "tag3",
				Value: "val3",
			}, {
				Key:   "tag4",
				Value: "undefined",
			}, {
				Key:   "tag5",
				Value: "val5",
			}, 
		},
			Fields: []FieldKeyValue{{
				Key:   "my-field-1",
				Value: float64(275),
			}, {
				Key:   "my-field-2",
				Value: float64(2048),
			}},
			Time: time.Unix(0, 1720618024249000000),
		}},
	}}

func TestDecoder(t *testing.T) {
	c := qt.New(t)
	for _, test := range decoderTests {
		c.Run(test.testName, func(c *qt.C) {
			errp, text := makeErrPositions(test.text)
			dec := NewDecoderWithBytesAndPopulate([]byte(text), "undefined")
			assertDecodeResult(c, dec, test.expect, false, errp)
		})
	}
}

// assertDecodeResult asserts that the entries from dec match
// the expected points and returns the number of points
// consumed. If allowMore is true, it's OK for there
// to be more points than expected.
func assertDecodeResult(c *qt.C, dec *Decoder, expect []Point, allowMore bool, errp errPositions) int {
	i := 0
	for {
		if i >= len(expect) && allowMore {
			return i
		}
		if !dec.Next() {
			break
		}
		if i >= len(expect) {
			c.Fatalf("too many points found")
		}
		for _, checkSection := range sectionCheckers {
			checkSection(c, dec, expect[i], errp)
		}
		i++
	}
	c.Assert(i, qt.Equals, len(expect))
	return i
}

func doSection(dec *Decoder, section section) error {
	switch section {
	case measurementSection:
		_, err := dec.Measurement()
		return err
	case tagSection:
		_, _, err := dec.NextTag()
		return err
	case fieldSection:
		_, _, _, err := dec.NextFieldBytes()
		return err
	case timeSection:
		_, err := dec.TimeBytes()
		return err
	}
	panic("unreachable")
}

// expectedSectionError returns the error that's expected when
// reading any complete section up to and including
// the given section.
func expectedSectionError(p Point, section section) string {
	switch section {
	case measurementSection:
		if p.MeasurementError != "" {
			return p.MeasurementError
		}
	case tagSection:
		for _, tag := range p.Tags {
			if tag.Error != "" {
				return tag.Error
			}
		}
	case fieldSection:
		for _, field := range p.Fields {
			if field.Error != "" {
				return field.Error
			}
		}
	case timeSection:
		if p.TimeError != "" {
			return p.TimeError
		}
	default:
		return ""
	}
	return expectedSectionError(p, section-1)
}

func TestDecoderSkipSection(t *testing.T) {
	// This test tests that we can call an individual decoder method
	// without calling any of the others. The decoder logic
	// should skip the other parts.
	c := qt.New(t)
	for _, test := range decoderTests {
		c.Run(test.testName, func(c *qt.C) {
			for secti := range sectionCheckers {
				sect := section(secti)
				c.Run(sect.String(), func(c *qt.C) {
					errp, text := makeErrPositions(test.text)
					dec := NewDecoderWithBytesAndPopulate([]byte(text), "undefined")
					i := 0
					for dec.Next() {
						if i >= len(test.expect) {
							continue
						}
						if e := expectedSectionError(test.expect[i], sect-1); e != "" && !strings.Contains(e, "cannot parse value for field key") {
							// If there's an error earlier in the line, it gets returned on the
							// later section (unless it's an error parsing a field value, in which case
							// the syntax checking is more lax).
							c.Assert(doSection(dec, sect), qt.ErrorMatches, regexp.QuoteMeta(errp.makeErr(e)))
						} else {
							sectionCheckers[sect](c, dec, test.expect[i], errp)
						}
						i++
					}
					c.Assert(i, qt.Equals, len(test.expect))
				})
			}
		})
	}
}

var decoderTakeTests = []struct {
	testName    string
	newDecoder  func(s string) *Decoder
	expectError string
}{{
	testName: "bytes",
	newDecoder: func(s string) *Decoder {
		return NewDecoderWithBytes([]byte(s))
	},
}, {
	testName: "reader",
	newDecoder: func(s string) *Decoder {
		return NewDecoder(strings.NewReader(s))
	},
}, {
	testName: "one-byte-reader",
	newDecoder: func(s string) *Decoder {
		return NewDecoder(iotest.OneByteReader(strings.NewReader(s)))
	},
}, {
	testName: "data-err-reader",
	newDecoder: func(s string) *Decoder {
		return NewDecoder(iotest.DataErrReader(strings.NewReader(s)))
	},
}, {
	testName: "error-reader",
	newDecoder: func(s string) *Decoder {
		return NewDecoder(&errorReader{
			r:   strings.NewReader(s),
			err: fmt.Errorf("some error"),
		})
	},
	expectError: "some error",
}}

// TestDecoderTake tests the internal Decoder.take method.
func TestDecoderTake(t *testing.T) {
	c := qt.New(t)
	for _, test := range decoderTakeTests {
		c.Run(test.testName, func(c *qt.C) {
			s := "aabbcccddefga"
			dec := test.newDecoder(s)
			data1 := dec.take(newByteSet("abc"))
			c.Assert(string(data1), qt.Equals, "aabbccc")

			data2 := dec.take(newByteSet("d"))
			c.Assert(string(data2), qt.Equals, "dd")

			data3 := dec.take(newByteSet(" ").invert())
			c.Assert(string(data3), qt.Equals, "efga")
			c.Assert(dec.complete, qt.Equals, true)

			data4 := dec.take(newByteSet(" ").invert())
			c.Assert(string(data4), qt.Equals, "")

			// Check that none of them have been overwritten.
			c.Assert(string(data1), qt.Equals, "aabbccc")
			c.Assert(string(data2), qt.Equals, "dd")
			c.Assert(string(data3), qt.Equals, "efga")
			if test.expectError != "" {
				c.Assert(dec.err, qt.ErrorMatches, test.expectError)
			} else {
				c.Assert(dec.err, qt.IsNil)
			}
		})
	}
}

func TestLongTake(t *testing.T) {
	c := qt.New(t)
	// Test that we can take segments that are longer than the
	// read buffer size.
	src := strings.Repeat("abcdefgh", (minRead*3)/8)
	dec := NewDecoder(strings.NewReader(src))
	data := dec.take(newByteSet("abcdefgh"))
	c.Assert(string(data), qt.Equals, src)
}

func TestTakeWithReset(t *testing.T) {
	c := qt.New(t)
	// Test that we can take segments that are longer than the
	// read buffer size.
	lineCount := (minRead * 3) / 9
	src := strings.Repeat("abcdefgh\n", lineCount)
	dec := NewDecoder(strings.NewReader(src))
	n := 0
	for {
		data := dec.take(newByteSet("abcdefgh"))
		if len(data) == 0 {
			break
		}
		n++
		c.Assert(string(data), qt.Equals, "abcdefgh")
		b := dec.at(0)
		c.Assert(b, qt.Equals, byte('\n'))
		dec.advance(1)
		dec.reset()
	}
	c.Assert(n, qt.Equals, lineCount)
}

func TestDecoderTakeWithReset(t *testing.T) {
	c := qt.New(t)
	// With a byte-at-a-time reader, we won't read any more
	// than we absolutely need.
	dec := NewDecoder(iotest.OneByteReader(strings.NewReader("aabbcccddefg")))
	data1 := dec.take(newByteSet("abc"))
	c.Assert(string(data1), qt.Equals, "aabbccc")
	c.Assert(dec.at(0), qt.Equals, byte('d'))
	dec.advance(1)
	dec.reset()
	c.Assert(dec.r0, qt.Equals, 0)
	c.Assert(dec.r1, qt.Equals, 0)
}

func TestDecoderTakeEsc(t *testing.T) {
	c := qt.New(t)
	for _, test := range decoderTakeTests {
		c.Run(test.testName, func(c *qt.C) {
			dec := test.newDecoder(`hello\ \t\\z\XY`)
			data, i, err := dec.takeEsc(newByteSet("X").invert(), &newEscaper(" \t").revTable)
			c.Assert(err, qt.IsNil)
			c.Assert(string(data), qt.Equals, "hello \t\\\\z\\")
			c.Assert(i, qt.Equals, 0)

			// Check that an escaped character will be included when
			// it's not part of the take set.
			dec = test.newDecoder(`hello\ \t\\z\XYX`)
			data1, i, err := dec.takeEsc(newByteSet("X").invert(), &newEscaper("X \t").revTable)
			c.Assert(err, qt.IsNil)
			c.Assert(string(data1), qt.Equals, "hello \t\\\\zXY")
			c.Assert(i, qt.Equals, 0)

			// Check that the next call to takeEsc continues where it left off.
			data2, i, err := dec.takeEsc(newByteSet(" ").invert(), &newEscaper(" ").revTable)
			c.Assert(err, qt.IsNil)
			c.Assert(string(data2), qt.Equals, "X")
			c.Assert(i, qt.Equals, 15)
			// Check that data1 hasn't been overwritten.
			c.Assert(string(data1), qt.Equals, "hello \t\\\\zXY")

			// Check that a backslash followed by EOF is taken as literal.
			dec = test.newDecoder(`x\`)
			data, i, err = dec.takeEsc(newByteSet("").invert(), &newEscaper(" ").revTable)
			c.Assert(err, qt.IsNil)
			c.Assert(i, qt.Equals, 0)
			c.Assert(string(data), qt.Equals, "x\\")
		})
	}
}

func TestDecoderTakeEscSkipping(t *testing.T) {
	c := qt.New(t)
	dec := NewDecoder(strings.NewReader(`hello\ \t\\z\XY`))
	dec.skipping = true
	data, i, err := dec.takeEsc(newByteSet("X").invert(), &newEscaper(" \t").revTable)
	c.Assert(err, qt.IsNil)
	// When skipping is true, the data isn't unquoted (that's just unnecessary extra work).
	c.Assert(string(data), qt.Equals, `hello\ \t\\z\`)
	c.Assert(i, qt.Equals, 0)
}

func TestDecoderTakeEscGrowBuffer(t *testing.T) {
	// This test tests the code path in Decoder.readMore
	// when the buffer needs to grow while we're reading a token.
	c := qt.New(t)
	dec := NewDecoder(&nbyteReader{
		buf: []byte(`hello\ \ \ \  foo`),
		next: []int{
			len(`hello\`),
			len(` \ \ \`),
			len(`  foo`),
		},
	})
	data, i, err := dec.takeEsc(newByteSet(" ").invert(), &newEscaper(" ").revTable)
	c.Assert(err, qt.IsNil)
	c.Assert(string(data), qt.Equals, `hello    `)
	c.Assert(i, qt.Equals, 0)
	data = dec.take(newByteSet("").invert())
	c.Assert(string(data), qt.Equals, ` foo`)
}

func TestDecoderTakeSlideBuffer(t *testing.T) {
	// This test tests the code path in Decoder.readMore
	// when the read buffer is large enough but the current
	// data is inconveniently in the wrong place, so
	// it gets slid to the front of the buffer.
	c := qt.New(t)
	// The first string that we'll read takes up almost all of the
	// initially added buffer, leaving just a little left at the end,
	// that will be moved to the front when we come to read that part.
	firstToken := strings.Repeat("a", minGrow-4)
	dec := NewDecoder(strings.NewReader(firstToken + ` helloworld there`))
	data := dec.take(newByteSet(" ").invert())
	c.Assert(string(data), qt.Equals, firstToken)
	data = dec.take(newByteSet(" "))
	c.Assert(string(data), qt.Equals, " ")

	// Reset the buffer. There's still the data from `helloworld` onwards that will remain in the buffer.
	dec.reset()

	data = dec.take(newByteSet(" ").invert())
	c.Assert(string(data), qt.Equals, "helloworld")
	data = dec.take(newByteSet(" "))
	c.Assert(string(data), qt.Equals, " ")
	data = dec.take(newByteSet(" ").invert())
	c.Assert(string(data), qt.Equals, "there")
}

type nbyteReader struct {
	// next holds the read counts for subsequent calls to Read.
	// If next is empty and buf is not empty, Read will panic.
	next []int
	// buf holds the data remaining to be read.
	buf []byte
}

func (r *nbyteReader) Read(buf []byte) (int, error) {
	if len(r.buf) == 0 && len(r.next) == 0 {
		return 0, io.EOF
	}
	n := r.next[0]
	r.next = r.next[1:]
	nb := copy(buf, r.buf[:n])
	if nb != n {
		panic(fmt.Errorf("read count for return (%d) is too large for provided buffer (%d)", n, len(r.buf)))
	}
	r.buf = r.buf[n:]
	return n, nil
}

type errorReader struct {
	r   io.Reader
	err error
}

func (r *errorReader) Read(buf []byte) (int, error) {
	n, err := r.r.Read(buf)
	if err != nil {
		err = r.err
	}
	return n, err
}

var scanEntriesBenchmarks = []struct {
	name     string
	makeData func() (data []byte, totalEntries int)
	expect   Point
}{{
	name: "long-lines",
	makeData: func() (data []byte, totalEntries int) {
		entry := `name,tag1=baz,tag2=asdfvdfsvdsvdfs,tagvdsvd=hello field="` + strings.Repeat("a", 4500) + `" 1602841605822791506`
		var buf bytes.Buffer
		for buf.Len() < 25*1024*1024 {
			buf.WriteString(entry)
			buf.WriteByte('\n')
			totalEntries++
		}
		return buf.Bytes(), totalEntries
	},
	expect: Point{
		Measurement: "name",
		Tags: []TagKeyValue{{
			Key:   "tag1",
			Value: "baz",
		}, {
			Key:   "tag2",
			Value: "asdfvdfsvdsvdfs",
		}, {
			Key:   "tagvdsvd",
			Value: "hello",
		}},
		Fields: []FieldKeyValue{{
			Key:   "field",
			Value: strings.Repeat("a", 4500),
		}},
		Time: time.Unix(0, 1602841605822791506),
	},
}, {
	name: "long-lines-with-escapes",
	makeData: func() (data []byte, totalEntries int) {
		entry := `name,ta\=g1=foo\ bar\,baz,tag2=asdfvdfsvdsvdfs,tag\=vdsvd=hello field="` + strings.Repeat(`\"`, 4500) + `" 1602841605822791506`
		var buf bytes.Buffer
		for buf.Len() < 25*1024*1024 {
			buf.WriteString(entry)
			buf.WriteByte('\n')
			totalEntries++
		}
		return buf.Bytes(), totalEntries
	},
	expect: Point{
		Measurement: "name",
		Tags: []TagKeyValue{{
			Key:   "ta=g1",
			Value: "foo bar,baz",
		}, {
			Key:   "tag2",
			Value: "asdfvdfsvdsvdfs",
		}, {
			Key:   "tag=vdsvd",
			Value: "hello",
		}},
		Fields: []FieldKeyValue{{
			Key:   "field",
			Value: strings.Repeat(`"`, 4500),
		}},
		Time: time.Unix(0, 1602841605822791506),
	},
}, {
	name: "single-short-line",
	makeData: func() ([]byte, int) {
		return []byte(`x,t=y y=1 1602841605822791506`), 1
	},
	expect: Point{
		Measurement: "x",
		Tags: []TagKeyValue{{
			Key:   "t",
			Value: "y",
		}},
		Fields: []FieldKeyValue{{
			Key:   "y",
			Value: 1.0,
		}},
		Time: time.Unix(0, 1602841605822791506),
	},
}, {
	name:     "single-short-line-with-escapes",
	makeData: singleEntry(`x,t=y\,y y=1 1602841605822791506`),
	expect: Point{
		Measurement: "x",
		Tags: []TagKeyValue{{
			Key:   "t",
			Value: "y,y",
		}},
		Fields: []FieldKeyValue{{
			Key:   "y",
			Value: 1.0,
		}},
		Time: time.Unix(0, 1602841605822791506),
	},
}, {
	name: "many-short-lines",
	makeData: func() (data []byte, totalEntries int) {
		entry := `x,t=y y=1 1602841605822791506`
		var buf bytes.Buffer
		for buf.Len() < 25*1024*1024 {
			buf.WriteString(entry)
			buf.WriteByte('\n')
			totalEntries++
		}
		return buf.Bytes(), totalEntries
	},
	expect: Point{
		Measurement: "x",
		Tags: []TagKeyValue{{
			Key:   "t",
			Value: "y",
		}},
		Fields: []FieldKeyValue{{
			Key:   "y",
			Value: 1.0,
		}},
		Time: time.Unix(0, 1602841605822791506),
	},
}, {
	name:     "field-key-escape-not-escapable",
	makeData: singleEntry(`cpu va\lue=42 1602841605822791506`),
	expect: Point{
		Measurement: "cpu",
		Fields: []FieldKeyValue{{
			Key:   `va\lue`,
			Value: 42.0,
		}},
		Time: time.Unix(0, 1602841605822791506),
	},
}, {
	name:     "tag-value-triple-escape-space",
	makeData: singleEntry(`cpu,host=two\\\ words value=42 1602841605822791506`),
	expect: Point{
		Measurement: "cpu",
		Tags: []TagKeyValue{{
			Key:   "host",
			Value: `two\\ words`,
		}},
		Fields: []FieldKeyValue{{
			Key:   `value`,
			Value: 42.0,
		}},
		Time: time.Unix(0, 1602841605822791506),
	},
}, {
	name:     "procstat",
	makeData: singleEntry(`procstat,exe=bash,process_name=bash voluntary_context_switches=42i,memory_rss=5103616i,rlimit_memory_data_hard=2147483647i,cpu_time_user=0.02,rlimit_file_locks_soft=2147483647i,pid=29417i,cpu_time_nice=0,rlimit_memory_locked_soft=65536i,read_count=259i,rlimit_memory_vms_hard=2147483647i,memory_swap=0i,rlimit_num_fds_soft=1024i,rlimit_nice_priority_hard=0i,cpu_time_soft_irq=0,cpu_time=0i,rlimit_memory_locked_hard=65536i,realtime_priority=0i,signals_pending=0i,nice_priority=20i,cpu_time_idle=0,memory_stack=139264i,memory_locked=0i,rlimit_memory_stack_soft=8388608i,cpu_time_iowait=0,cpu_time_guest=0,cpu_time_guest_nice=0,rlimit_memory_data_soft=2147483647i,read_bytes=0i,rlimit_cpu_time_soft=2147483647i,involuntary_context_switches=2i,write_bytes=106496i,cpu_time_system=0,cpu_time_irq=0,cpu_usage=0,memory_vms=21659648i,memory_data=1576960i,rlimit_memory_stack_hard=2147483647i,num_threads=1i,rlimit_memory_rss_soft=2147483647i,rlimit_realtime_priority_soft=0i,num_fds=4i,write_count=35i,rlimit_signals_pending_soft=78994i,cpu_time_steal=0,rlimit_num_fds_hard=4096i,rlimit_file_locks_hard=2147483647i,rlimit_cpu_time_hard=2147483647i,rlimit_signals_pending_hard=78994i,rlimit_nice_priority_soft=0i,rlimit_memory_rss_hard=2147483647i,rlimit_memory_vms_soft=2147483647i,rlimit_realtime_priority_hard=0i 1517620624000000000`),
	expect: Point{
		Measurement: "procstat",
		Tags: []TagKeyValue{{
			Key:   "exe",
			Value: "bash",
		}, {
			Key:   "process_name",
			Value: "bash",
		}},
		Fields: []FieldKeyValue{{
			Key:   "voluntary_context_switches",
			Value: int64(42),
		}, {
			Key:   "memory_rss",
			Value: int64(5103616),
		}, {
			Key:   "rlimit_memory_data_hard",
			Value: int64(2147483647),
		}, {
			Key:   "cpu_time_user",
			Value: 0.02,
		}, {
			Key:   "rlimit_file_locks_soft",
			Value: int64(2147483647),
		}, {
			Key:   "pid",
			Value: int64(29417),
		}, {
			Key:   "cpu_time_nice",
			Value: 0.0,
		}, {
			Key:   "rlimit_memory_locked_soft",
			Value: int64(65536),
		}, {
			Key:   "read_count",
			Value: int64(259),
		}, {
			Key:   "rlimit_memory_vms_hard",
			Value: int64(2147483647),
		}, {
			Key:   "memory_swap",
			Value: int64(0),
		}, {
			Key:   "rlimit_num_fds_soft",
			Value: int64(1024),
		}, {
			Key:   "rlimit_nice_priority_hard",
			Value: int64(0),
		}, {
			Key:   "cpu_time_soft_irq",
			Value: 0.0,
		}, {
			Key:   "cpu_time",
			Value: int64(0),
		}, {
			Key:   "rlimit_memory_locked_hard",
			Value: int64(65536),
		}, {
			Key:   "realtime_priority",
			Value: int64(0),
		}, {
			Key:   "signals_pending",
			Value: int64(0),
		}, {
			Key:   "nice_priority",
			Value: int64(20),
		}, {
			Key:   "cpu_time_idle",
			Value: 0.0,
		}, {
			Key:   "memory_stack",
			Value: int64(139264),
		}, {
			Key:   "memory_locked",
			Value: int64(0),
		}, {
			Key:   "rlimit_memory_stack_soft",
			Value: int64(8388608),
		}, {
			Key:   "cpu_time_iowait",
			Value: 0.0,
		}, {
			Key:   "cpu_time_guest",
			Value: 0.0,
		}, {
			Key:   "cpu_time_guest_nice",
			Value: 0.0,
		}, {
			Key:   "rlimit_memory_data_soft",
			Value: int64(2147483647),
		}, {
			Key:   "read_bytes",
			Value: int64(0),
		}, {
			Key:   "rlimit_cpu_time_soft",
			Value: int64(2147483647),
		}, {
			Key:   "involuntary_context_switches",
			Value: int64(2),
		}, {
			Key:   "write_bytes",
			Value: int64(106496),
		}, {
			Key:   "cpu_time_system",
			Value: 0.0,
		}, {
			Key:   "cpu_time_irq",
			Value: 0.0,
		}, {
			Key:   "cpu_usage",
			Value: 0.0,
		}, {
			Key:   "memory_vms",
			Value: int64(21659648),
		}, {
			Key:   "memory_data",
			Value: int64(1576960),
		}, {
			Key:   "rlimit_memory_stack_hard",
			Value: int64(2147483647),
		}, {
			Key:   "num_threads",
			Value: int64(1),
		}, {
			Key:   "rlimit_memory_rss_soft",
			Value: int64(2147483647),
		}, {
			Key:   "rlimit_realtime_priority_soft",
			Value: int64(0),
		}, {
			Key:   "num_fds",
			Value: int64(4),
		}, {
			Key:   "write_count",
			Value: int64(35),
		}, {
			Key:   "rlimit_signals_pending_soft",
			Value: int64(78994),
		}, {
			Key:   "cpu_time_steal",
			Value: 0.0,
		}, {
			Key:   "rlimit_num_fds_hard",
			Value: int64(4096),
		}, {
			Key:   "rlimit_file_locks_hard",
			Value: int64(2147483647),
		}, {
			Key:   "rlimit_cpu_time_hard",
			Value: int64(2147483647),
		}, {
			Key:   "rlimit_signals_pending_hard",
			Value: int64(78994),
		}, {
			Key:   "rlimit_nice_priority_soft",
			Value: int64(0),
		}, {
			Key:   "rlimit_memory_rss_hard",
			Value: int64(2147483647),
		}, {
			Key:   "rlimit_memory_vms_soft",
			Value: int64(2147483647),
		}, {
			Key:   "rlimit_realtime_priority_hard",
			Value: int64(0),
		}},
		Time: time.Unix(0, 1517620624000000000),
	},
}}

func singleEntry(s string) func() ([]byte, int) {
	return func() ([]byte, int) {
		return []byte(s), 1
	}
}

func BenchmarkDecodeEntriesSkipping(b *testing.B) {
	for _, bench := range scanEntriesBenchmarks {
		b.Run(bench.name, func(b *testing.B) {
			data, total := bench.makeData()
			c := qt.New(b)
			// Sanity check that the decoder is doing what we're expecting.
			// Only check the first entry because checking them all is slow.
			dec := NewDecoderWithBytes(data)
			assertDecodeResult(c, dec, []Point{bench.expect}, true, errPositions{})
			b.ReportAllocs()
			b.ResetTimer()
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				n := 0
				dec := NewDecoderWithBytes(data)
				for dec.Next() {
					n++
				}
				if n != total {
					b.Fatalf("unexpected read count; got %v want %v", n, total)
				}
			}
		})
	}
}

func BenchmarkDecodeEntriesWithoutSkipping(b *testing.B) {
	for _, bench := range scanEntriesBenchmarks {
		b.Run(bench.name, func(b *testing.B) {
			data, total := bench.makeData()
			c := qt.New(b)
			// Sanity check that the decoder is doing what we're expecting.
			// Only check the first entry because checking them all is slow.
			dec := NewDecoderWithBytes(data)
			assertDecodeResult(c, dec, []Point{bench.expect}, true, errPositions{})
			b.ReportAllocs()
			b.ResetTimer()
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				n := 0
				dec := NewDecoderWithBytes(data)
				for dec.Next() {
					dec.Measurement()
					for {
						key, _, _ := dec.NextTag()
						if key == nil {
							break
						}
					}
					for {
						key, _, _ := dec.NextField()
						if key == nil {
							break
						}
					}
					dec.TimeBytes()
					n++
				}
				if n != total {
					b.Fatalf("unexpected read count; got %v want %v", n, total)
				}
			}
		})
	}
}

type errPos struct {
	line   int64
	column int
}

func (p errPos) String() string {
	return fmt.Sprintf("%d:%d", p.line, p.column)
}

// errPositions records error positions so that we can avoid
// mentioning them directly in test cases.
type errPositions struct {
	repl *strings.Replacer
}

// makeErr returns s with ∑ markers replaced with
// their line:column equivalents.
func (errp errPositions) makeErr(s string) string {
	if errp.repl == nil {
		return s
	}
	s1 := errp.repl.Replace(s)
	if strings.Count(s1, "∑") > 0 {
		panic(fmt.Errorf("no value for error marker found in %q (remaining %q)", s, s1))
	}
	return s1
}

// makeErrPositions returns the positions of all the ∑ markers
// in the text keyed by the character following the ∑.
//
// It also returns the text with the ∑ markers removed.
func makeErrPositions(text string) (errPositions, string) {
	buf := make([]byte, 0, len(text))
	currPos := errPos{
		line:   1,
		column: 1,
	}
	var repls []string
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		switch r {
		case '\n':
			currPos.line++
			currPos.column = 1
			buf = append(buf, '\n')
		case '∑':
			_, size = utf8.DecodeRuneInString(text[size:])
			repls = append(repls, text[:len("∑")+size], currPos.String())
			text = text[len("∑"):]
		default:
			buf = append(buf, text[:size]...)
			currPos.column += size
		}
		text = text[size:]
	}
	return errPositions{
		repl: strings.NewReplacer(repls...),
	}, string(buf)
}

func TestErrorPositions(t *testing.T) {
	// Although this is just testing an internal testing function,
	// that function itself is used as the basis for other tests,
	// so we're providing some base assurance for those.
	tests := []struct {
		text       string
		err        string
		expectErr  string
		expectText string
	}{{
		text:      "",
		err:       "",
		expectErr: "",
	}, {
		text:      "∑¹",
		err:       "at line ∑¹: something",
		expectErr: "at line 1:1: something",
	}, {
		text:       "a\nbác∑¹helélo∑²blah\n∑³x\n",
		err:        "foo: at line ∑¹: blah",
		expectErr:  "foo: at line 2:5: blah",
		expectText: "a\nbácheléloblah\nx\n",
	}, {
		text:       "a\nbác∑¹helélo∑²blah\n∑³x\n",
		err:        "foo: at line ∑²: blah",
		expectErr:  "foo: at line 2:12: blah",
		expectText: "a\nbácheléloblah\nx\n",
	}, {
		text:       "a\nbác∑¹helélo∑²blah\n∑³x\n",
		err:        "foo: at line ∑³: blah",
		expectErr:  "foo: at line 3:1: blah",
		expectText: "a\nbácheléloblah\nx\n",
	}, {
		text:       "a∑¹x",
		err:        "at line ∑¹: blah",
		expectErr:  "at line 1:2: blah",
		expectText: "ax",
	}}
	c := qt.New(t)
	for _, test := range tests {
		c.Run("", func(c *qt.C) {
			eps, text := makeErrPositions(test.text)
			c.Assert(text, qt.Equals, test.expectText)
			c.Assert(eps.makeErr(test.err), qt.Equals, test.expectErr)
		})
	}
}

func TestDecodeLargeDataWithReader(t *testing.T) {
	c := qt.New(t)
	r, w := io.Pipe()
	const maxTagCount = 9
	const maxFieldCount = 4
	const npoints = 2000
	go func() {
		defer w.Close()
		bw := bufio.NewWriter(w)
		defer bw.Flush()
		g := newTokenGenerator()
		var enc Encoder
		enc.SetLax(true) // Allow out-of-order tag keys.
		for i := 0; i < npoints; i++ {
			ntags := g.rand.Intn(maxTagCount + 1)
			nfields := g.rand.Intn(maxFieldCount) + 1
			timestamp := g.rand.Int63n(0xffff_ffff_ffff)
			enc.StartLineRaw(g.token())
			for j := 0; j < ntags; j++ {
				enc.AddTagRaw(g.token(), g.token())
			}
			for j := 0; j < nfields; j++ {
				key, val := g.token(), g.token()
				v, err := NewValueFromBytes(String, val)
				if err != nil {
					panic(err)
				}
				enc.AddFieldRaw(key, v)
			}
			enc.EndLine(time.Unix(0, timestamp))
			bw.Write(enc.Bytes())
			enc.Reset()
		}
	}()
	g := newTokenGenerator()
	var wc writeCounter
	dec := NewDecoder(io.TeeReader(r, &wc))
	n := 0
	for ; dec.Next(); n++ {
		if n >= npoints {
			c.Fatalf("too many points decoded")
		}
		wantNtags := g.rand.Intn(maxTagCount + 1)
		wantNfields := g.rand.Intn(maxFieldCount) + 1
		wantTimestamp := g.rand.Int63n(0xffff_ffff_ffff)
		m, err := dec.Measurement()
		c.Assert(err, qt.IsNil)
		c.Check(m, qt.DeepEquals, g.token(), qt.Commentf("n %d", n))
		tagi := 0
		for {
			key, val, err := dec.NextTag()
			c.Assert(err, qt.IsNil)
			if key == nil {
				break
			}
			if tagi >= wantNtags {
				c.Fatalf("too many tags found on entry %d", n)
			}
			wantKey, wantVal := g.token(), g.token()
			c.Check(key, qt.DeepEquals, wantKey)
			c.Check(val, qt.DeepEquals, wantVal)
			tagi++
		}
		c.Assert(tagi, qt.Equals, wantNtags)
		fieldi := 0
		for {
			key, val, err := dec.NextField()
			c.Assert(err, qt.IsNil)
			if key == nil {
				break
			}
			if fieldi >= wantNfields {
				c.Fatalf("too many tags found on entry %d", n)
			}
			wantKey, wantVal := g.token(), g.token()
			c.Check(key, qt.DeepEquals, wantKey)
			c.Check(val.Interface(), qt.Equals, string(wantVal))
			fieldi++
		}
		c.Assert(fieldi, qt.Equals, wantNfields)
		t, err := dec.Time(Nanosecond, time.Time{})
		c.Check(err, qt.IsNil)
		c.Check(t.UnixNano(), qt.Equals, wantTimestamp)
	}
	c.Assert(n, qt.Equals, npoints)
	c.Logf("total bytes: %v", wc.n)
}

type writeCounter struct {
	n int
}

func (w *writeCounter) Write(buf []byte) (int, error) {
	w.n += len(buf)
	return len(buf), nil
}

func newTokenGenerator() *tokenGenerator {
	return &tokenGenerator{
		rand: rand.New(rand.NewSource(0)),
	}
}

type tokenGenerator struct {
	rand *rand.Rand
}

const alphabet = "abcdefghijklmnopqrstuvwxyz =, =, =,"

func (g *tokenGenerator) token() []byte {
	data := make([]byte, g.rand.Intn(40)+1)
	for i := range data {
		data[i] = alphabet[g.rand.Intn(len(alphabet))]
	}
	return data
}
