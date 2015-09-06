// Eac2json parses Schwab's Employee Awards Center transaction
// history page from HTML into JSON. The output is a sequence of
// entries, each of which is a key-value bag. They are in the same
// order as they appear in the input document. Eac2json coalesces
// "more details" entries into its parent entry; their key-values
// appear together as one.
//
// In order to use eac2json, you must first save the HTML from the
// Schwab Employee Awards Center history page. Go to  the "My Equity
// Awards" tab, then to the "History & Statements" sub-tab. Set the
// date range to "All", and save the page to a file. Chrome doesn't
// seem to do very well; Safari works fine.
//
// NB! Eac2json is intended to assist in computing wash sales only.
// It ignores certain entries that are not relevant for these purposes.
//
// What follows are the record types (as named by the "Action" field),
// to the best of my understanding.
//
// - "Deposit": a deposit into the EAC account. Schwab will
// deposit RSU shares into the EAC account before automatically
// selling them ("Forced Quick Sell") for tax purposes.
//
// - "Forced Quick Sell": a sale of RSUs for tax purposes.
//
// - "Lapse": a lapse of RSUs. The amount in "Net Shares Deposited"
// goes into your Schwab brokerage account, the remainder is
// sold for tax purposes, as chronicled by "Deposit" and
// "Forced Quick Sell" entries.
//
// - "Exer and Hold": option (ISO or NSO) excercise-and-holds.
// The shares are deposited into your broker account.
//
// - "Sale": Option or ESPP sales. (Exercise and sell.)
//
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"golang.org/x/net/html"
)

var coreKeys = []string{
	"Date",
	"Description",
	"Action",
	"Symbol"}

type Node struct {
	*html.Node
	next *html.Node
	err  error

	stack *Node
}

func (n *Node) Sibling(tag string) {
	if n.err != nil {
		return
	}

	for c := n.next; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == tag {
			n.Node = c
			n.next = c.NextSibling
			return
		}
	}

	n.err = errors.New(fmt.Sprintf("no sibling tag %s", tag))
}

func (n *Node) Child(tag string) {
	if n.err != nil {
		return
	}

	n.Node = n.FirstChild
	n.next = n.Node
	n.Sibling(tag)
}

// Root out the next (child) text node without advancing the cursor.
func (n *Node) ChildText() string {
	if n.err != nil {
		return ""
	}

	for c := n.Node; c != nil; c = c.FirstChild {
		if c.Type == html.TextNode {
			return c.Data
		}
	}

	return ""
}

func (n *Node) Text() string {
	if n.err != nil {
		return ""
	}

	for c := n.Node; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			return c.Data
		}
	}

	return ""
}

func (n *Node) Push() {
	m := new(Node)
	*m = *n
	n.stack = m
}

func (n *Node) Pop() {
	*n = *n.stack
}

func (n *Node) Err() error {
	return n.err
}

func (n *Node) Ok() bool {
	return n.err == nil
}

type Ledger struct {
	entries []map[string]string
	e       map[string]string
}

func (l *Ledger) Next() {
	if len(l.e) > 0 {
		l.entries = append(l.entries, l.e)
	}
	l.e = make(map[string]string)
}

func (l *Ledger) Write(k, v string) {
	l.e[k] = v
}

func findHistory(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.Data == "a" {
		for _, a := range n.Attr {
			if a.Key == "name" && a.Val == "History" {
				return n
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		n := findHistory(c)
		if n != nil {
			return n
		}
	}

	return nil
}

// Extract a regular data row.
func row(n *Node) ([]string, error) {
	n.Push()
	defer n.Pop()

	var values []string
	for n.Child("td"); n.Ok(); n.Sibling("td") {
		n.Push()
		n.Child("label")
		val := strings.TrimSpace(n.ChildText())
		if !n.Ok() {
			err := n.Err()
			n.Pop()
			return nil, err
		}

		n.Pop()
		values = append(values, val)
	}

	return values, nil
}

// Extract a "more details" row set.
func more(n *Node) ([]map[string]string, error) {
	n.Push()
	defer n.Pop()

	n.Child("td")
	n.Child("div")
	n.Child("div")
	n.Child("table")
	n.Sibling("table")
	n.Child("tbody")

	// First row is headers.
	n.Child("tr")

	if !n.Ok() {
		return nil, n.Err()
	}

	var headers []string

	n.Push()
	for n.Child("td"); n.Ok(); n.Sibling("td") {
		n.Push()
		n.Child("b")
		headers = append(headers, strings.TrimSpace(n.ChildText()))
		n.Pop()
	}
	n.Pop()

	for len(headers) > 0 && headers[len(headers)-1] == "" {
		headers = headers[:len(headers)-1]
	}

	var entries []map[string]string

	for n.Sibling("tr"); n.Ok(); n.Sibling("tr") {
		n.Push()

		m := make(map[string]string)

		var i int
		n.Child("td")
		for i = 0; n.Ok() && i < len(headers); i++ {
			m[headers[i]] = strings.TrimSpace(n.ChildText())
			n.Sibling("td")
		}

		if i == len(headers) {
			entries = append(entries, m)
		}

		n.Pop()
	}

	return entries, nil
}

// Extract the second style of "more details" row.
func more1(n *Node) (map[string]string, error) {
	n.Push()
	defer n.Pop()

	n.Child("td")
	n.Child("div")
	n.Child("div")
	n.Child("table")
	n.Sibling("table")
	n.Child("tbody")

	if !n.Ok() {
		return nil, n.Err()
	}

	entries := make(map[string]string)

	for n.Child("tr"); n.Ok(); n.Sibling("tr") {
		n.Push()

		for n.Child("td"); n.Ok(); n.Sibling("td") {
			key := strings.TrimSpace(n.ChildText())
			n.Push()
			n.Child("b")
			value := strings.TrimSpace(n.Text())
			n.Pop()

			if key != "" {
				entries[key] = value
			}
		}

		n.Pop()
	}

	return entries, nil
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: eac2json [file]\n")
	os.Exit(2)
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("eac2json: ")

	var r io.Reader

	switch len(os.Args) {
	case 1:
		r = os.Stdin
	case 2:
		file, err := os.Open(os.Args[1])
		if err != nil {
			log.Fatal(err)
		}
		r = file
	default:
		usage()
	}

	doc, err := html.Parse(r)
	if err != nil {
		log.Fatal(err)
	}

	// First find the transaction history table.
	root := findHistory(doc)
	if root == nil {
		log.Fatal("no history")
	}

	// Grub out the actual table body
	// from the root of the transaction table.
	n := &Node{root, root, nil, nil}
	n.Child("table")
	n.Child("tbody")
	n.Child("tr")
	n.Sibling("tr")
	n.Child("td")
	n.Child("table")
	n.Child("tbody")

	if n.Type != html.ElementNode || n.Data != "tbody" {
		log.Fatalf("bad table node %v type %d data %s", n, n.Type, n.Data)
	}

	n.Child("tr")

	if !n.Ok() {
		log.Fatal(n.Err())
	}

	// The first row is the header
	headerVals, err := row(n)
	if err != nil {
		log.Fatalf("no header: %s", err)
	}

	header := make(map[string]int)
	for i := range headerVals {
		header[headerVals[i]] = i
	}

	var l Ledger

	for n.Sibling("tr"); n.Ok(); n.Sibling("tr") {
		// First try to extract a regular data row.
		values, err := row(n)
		if err != nil {
			log.Fatalf("bad row: %s", err)
		}

		switch values[header["Action"]] {
		case "Lapse":
			l.Next()

			for k, i := range header {
				l.Write(k, values[i])
			}

			n.Sibling("tr")
			entries, err := more1(n)
			if err != nil {
				log.Fatal(err)
			}

			for k, v := range entries {
				l.Write(k, v)
			}

		case "Deposit", "Forced Quick Sell":
			// Schwab sells shares for taxes by first depositing them
			// to your EAC account,and then selling them.
			// Remaining shares go into your brokerage account.

			// Incorporate data into the row.
			l.Next()

			for k, i := range header {
				l.Write(k, values[i])
			}

			n.Sibling("tr")
			entries, err := more(n)
			if err != nil {
				log.Fatal(err)
			}
			if len(entries) != 1 {
				log.Fatalf("Expected one row; got %d", len(entries))
			}

			for k, v := range entries[0] {
				l.Write(k, v)
			}

		case "Exer and Hold", "Sale":
			// ISO exercise and hold. The details pane here may
			// contain multiple entries that have different prices.
			// We break this up into multiple entries.
			//
			// "Sale" is for other sales.

			// XXX looks like ESPPs are sold directly in the brokeage account.
			// XXX take care of this next

			n.Sibling("tr")
			entries, err := more(n)
			if err != nil {
				log.Fatal(err)
			}
			if len(entries) == 0 {
				log.Fatalf("empty \"more details\" for Exer and Hold")
			}

			for _, e := range entries {
				l.Next()

				for _, k := range coreKeys {
					l.Write(k, values[header[k]])
				}

				for k, v := range e {
					l.Write(k, v)
				}
			}

		case "Journal":
			// The next row holds more details, but it's not useful to us.
			n.Sibling("tr")

		case "Forced Disbursement":
			// Not relevant for our purposes. Also they don't contain any
			// extra rows.

		default:
			log.Fatalf(fmt.Sprintf("unknown row type \"%s\"", values[header["Action"]]))
		}
	}

	// TODO: check that we're at the end of the table;
	// that there are no more rows.

	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	enc := json.NewEncoder(w)
	if err := enc.Encode(l.entries); err != nil {
		log.Fatal(err)
	}
}
