package rag

import (
	"database/sql"
	"encoding/binary"
	"math"
	"testing"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"github.com/stretchr/testify/require"
)

func TestSqliteVec_Available(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	var version string
	err = db.QueryRow("SELECT vec_version()").Scan(&version)
	require.NoError(t, err, "vec_version() should be available")
	t.Logf("sqlite-vec version: %s", version)
}

func TestSqliteVec_CreateVec0Table(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`CREATE VIRTUAL TABLE test_vec USING vec0(
		embedding float[4] distance_metric=cosine
	)`)
	require.NoError(t, err, "should be able to create vec0 table")
}

func TestSqliteVec_InsertAndQuery(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`CREATE VIRTUAL TABLE test_vec USING vec0(
		embedding float[4] distance_metric=cosine,
		category TEXT
	)`)
	require.NoError(t, err)

	// Insert vectors
	vec1 := serializeFloat32([]float32{1.0, 0.0, 0.0, 0.0})
	vec2 := serializeFloat32([]float32{0.0, 1.0, 0.0, 0.0})
	vec3 := serializeFloat32([]float32{0.9, 0.1, 0.0, 0.0})

	_, err = db.Exec("INSERT INTO test_vec(rowid, embedding, category) VALUES (?, ?, ?)", 1, vec1, "a")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO test_vec(rowid, embedding, category) VALUES (?, ?, ?)", 2, vec2, "b")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO test_vec(rowid, embedding, category) VALUES (?, ?, ?)", 3, vec3, "a")
	require.NoError(t, err)

	// KNN query
	query := serializeFloat32([]float32{1.0, 0.0, 0.0, 0.0})
	rows, err := db.Query(`
		SELECT rowid, distance, category
		FROM test_vec
		WHERE embedding MATCH ? AND k = 3
		ORDER BY distance
	`, query)
	require.NoError(t, err)
	defer rows.Close()

	var results []int64
	for rows.Next() {
		var id int64
		var dist float64
		var cat string
		require.NoError(t, rows.Scan(&id, &dist, &cat))
		results = append(results, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []int64{1, 3, 2}, results, "cosine KNN should return nearest vectors first")
}

// serializeFloat32 converts []float32 to little-endian byte slice for vec0 BLOB storage.
func serializeFloat32(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}
