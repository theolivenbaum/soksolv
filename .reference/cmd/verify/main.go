// Command lowverify replays a LURD solution (stdin) on one level of an .xsb collection with the frozen
// verify package: lowverify FILE INDEX < lurd. Prints "OK" or "FAIL: reason" (exit 0 either way; exit 2 on usage
// errors). Used by diag/remote/lowq.py (exp-440).
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"soksolv/verify"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: lowverify FILE INDEX < lurd")
		os.Exit(2)
	}
	idx, err := strconv.Atoi(os.Args[2])
	data, err2 := os.ReadFile(os.Args[1])
	if err != nil || err2 != nil {
		fmt.Fprintln(os.Stderr, "bad arguments")
		os.Exit(2)
	}
	lvs := verify.SplitLevels(data)
	if idx < 1 || idx > len(lvs) {
		fmt.Println("FAIL: level index out of range")
		return
	}
	sol, _ := io.ReadAll(os.Stdin)
	if e := verify.CheckSolution(lvs[idx-1], strings.TrimSpace(string(sol))); e != nil {
		fmt.Println("FAIL:", e)
		return
	}
	fmt.Println("OK")
}
