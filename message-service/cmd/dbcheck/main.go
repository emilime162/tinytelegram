package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	_ "github.com/lib/pq"
)

// dbcheck runs the Phase 1 exit-criterion verification queries against the
// cloud RDS via the SSM tunnel (no psql client needed on the host). It prints
// total message count, duplicate PTS checks, and large receiver_pts gaps.
func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatal("POSTGRES_DSN not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("ping: %v", err)
	}

	var total int64
	if err := db.QueryRow(`SELECT count(*) FROM messages`).Scan(&total); err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("=== total_messages: %d ===\n\n", total)

	fmt.Println("=== duplicate receiver_pts (expect 0) ===")
	printRows(db, `
        SELECT receiver_id, receiver_pts, count(*) AS dups
        FROM messages
        GROUP BY 1,2
        HAVING count(*) > 1
        LIMIT 10`, 3)

	fmt.Println("\n=== duplicate sender_pts (expect 0) ===")
	printRows(db, `
        SELECT sender_id, sender_pts, count(*) AS dups
        FROM messages
        GROUP BY 1,2
        HAVING count(*) > 1
        LIMIT 10`, 3)

	fmt.Println("\n=== receiver_pts gaps > 1 (expect small if any; >1 means a WAIT-rejected message left a hole) ===")
	printRows(db, `
        WITH ordered AS (
          SELECT receiver_id, receiver_pts,
                 LAG(receiver_pts) OVER (PARTITION BY receiver_id ORDER BY receiver_pts) AS prev
          FROM messages
        )
        SELECT receiver_id, prev, receiver_pts, (receiver_pts - prev) AS gap
        FROM ordered
        WHERE receiver_pts - prev > 1
        ORDER BY gap DESC
        LIMIT 20`, 4)
}

func printRows(db *sql.DB, q string, ncols int) {
	rows, err := db.Query(q)
	if err != nil {
		log.Fatalf("query: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		vals := make([]any, ncols)
		ptrs := make([]any, ncols)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			log.Fatalf("scan: %v", err)
		}
		fmt.Printf("  %v\n", vals)
		n++
	}
	if n == 0 {
		fmt.Println("  (0 rows)")
	}
}
