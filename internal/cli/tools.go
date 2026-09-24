package cli

// ToolInfo describes one tool of the family, for man pages and overviews.
type ToolInfo struct {
	Name  string
	Like  string // the classic tool it mirrors
	Short string // one-line description (man NAME)
}

// Tools is the grev family, in the order they are presented.
var Tools = []ToolInfo{
	{"grev", "grep", "print the records a model answers yes to"},
	{"isv", "test", "answer a yes/no question about the input with the exit status"},
	{"oneof", "case", "print which label best describes the input"},
	{"tagv", "awk '{print > $1}'", "label every record with the label that fits it best"},
	{"rank", "sort", "sort records by how well they fit a query"},
	{"pickv", "grep -o, head -1", "print the line or regex match that best answers a question"},
	{"uniqv", "uniq", "collapse adjacent records that mean the same thing"},
	{"unwrap", "fmt", "rejoin lines that were hard-wrapped mid-sentence"},
	{"seg", "csplit", "split a stream into segments where the topic changes"},
	{"cutv", "cut", "cut the table columns that match descriptions"},
	{"seek", "find, cd", "find the path in a tree that matches a description"},
	{"probev", "awk", "print per-record answer columns for many questions"},
	{"lookv", "look, git bisect", "binary-search ordered records for where an answer flips"},
	{"trv", "tr, sed s///", "translate, delete, squeeze or replace only where an instruction applies"},
	{"sortv", "sort", "sort records in an order described in words, by pairwise comparison"},
	{"jev", "curl", "configure, inspect and query the Jev API behind the grev tools"},
}

func toolShort(name string) string {
	for _, t := range Tools {
		if t.Name == name {
			return t.Short
		}
	}
	return ""
}
