package spool

import "fmt"

func testRecordID(n int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", n)
}
