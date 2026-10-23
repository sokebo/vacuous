// Package scripts holds the guards for the helper scripts in scripts/.
//
// The scripts are what people actually run, so what they say about a report
// is part of the tool's output. A script that says "nothing to see here"
// about a report carrying high-severity findings is worse than no script at
// all: it is a false all-clear, and it is the exact confusion this tool
// exists to catch - in its own wrapper.
//
// The guards here run the real script against a report produced by the
// tool's own report package. That matters: if the tool renames a field in
// its JSON, the script starts reading zeros, and the script's conclusion
// stops matching the report it was summarizing. Only a test that feeds the
// script the tool's actual output can notice that.
package scripts
