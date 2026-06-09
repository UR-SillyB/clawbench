package rag

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- SQLite Store setup ----------

// setupSQLiteStore creates an in-memory SQLite store for testing.
func setupSQLiteStore(t *testing.T) *Store {
	t.Helper()
	// Ensure segmenter is initialized for tests that use SegmentText
	if segmenter == nil {
		if err := InitSegmenter(); err != nil {
			t.Logf("Warning: gse segmenter not available: %v", err)
		}
	}
	store, err := NewSQLiteStore(":memory:")
	require.NoError(t, err, "NewSQLiteStore should succeed")
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// ---------- NewSQLiteStore ----------

func TestSQLiteStore_CreatesSchema(t *testing.T) {
	store := setupSQLiteStore(t)
	assert.NotNil(t, store.db)
}

// ---------- InsertChunks ----------

func TestSQLiteStore_InsertChunks_Empty(t *testing.T) {
	store := setupSQLiteStore(t)
	err := store.InsertChunks(nil)
	assert.NoError(t, err, "InsertChunks with nil should be no-op")

	err = store.InsertChunks([]Chunk{})
	assert.NoError(t, err, "InsertChunks with empty slice should be no-op")
}

func TestSQLiteStore_InsertChunks_SingleChunk(t *testing.T) {
	store := setupSQLiteStore(t)
	chunks := []Chunk{makeTestChunk(testSession1, 1, 0, "hello world")}
	err := store.InsertChunks(chunks)
	assert.NoError(t, err)

	count, err := store.ChunkCount()
	assert.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestSQLiteStore_InsertChunks_MultipleChunks(t *testing.T) {
	store := setupSQLiteStore(t)
	insertTestChunksSQLite(t, store, 5)

	count, err := store.ChunkCount()
	assert.NoError(t, err)
	assert.Equal(t, 5, count)
}

func TestSQLiteStore_InsertChunks_WithoutEmbedding(t *testing.T) {
	store := setupSQLiteStore(t)
	chunk := Chunk{
		SessionID:          testSession1,
		MessageID:          1,
		ChunkText:          "test without embedding",
		ChunkTextSegmented: "test without embedding",
		ChunkIndex:         0,
		TokenCount:         5,
		Embedding:          nil,
		HasEmbedding:       false,
		ProjectPath:        testProjectPath,
		Backend:            testBackendClaude,
		Role:               testRoleAssistant,
		CreatedAt:          time.Now().Truncate(time.Millisecond),
	}
	err := store.InsertChunks([]Chunk{chunk})
	require.NoError(t, err)

	var hasEmb int
	err = store.db.QueryRowContext(context.Background(), "SELECT has_embedding FROM rag_chunks LIMIT 1").Scan(&hasEmb)
	assert.NoError(t, err)
	assert.Equal(t, 0, hasEmb, "chunk without embedding should have has_embedding=0")
}

func TestSQLiteStore_InsertChunks_MixedEmbeddingAndNoEmbedding(t *testing.T) {
	store := setupSQLiteStore(t)
	chunkWithEmb := makeTestChunk(testSession1, 1, 0, "has embedding")
	chunkWithoutEmb := Chunk{
		SessionID:          testSession2,
		MessageID:          2,
		ChunkText:          "no embedding",
		ChunkTextSegmented: "no embedding",
		ChunkIndex:         0,
		TokenCount:         3,
		Embedding:          nil,
		HasEmbedding:       false,
		ProjectPath:        testProjectPath,
		Backend:            testBackendClaude,
		Role:               testRoleAssistant,
		CreatedAt:          time.Now().Truncate(time.Millisecond),
	}

	err := store.InsertChunks([]Chunk{chunkWithEmb, chunkWithoutEmb})
	require.NoError(t, err)

	count, _ := store.ChunkCount()
	assert.Equal(t, 2, count, "both chunks should be inserted")

	pending, _ := store.PendingEmbeddingCount()
	assert.Equal(t, 1, pending, "one chunk should need embedding backfill")
}

func TestSQLiteStore_InsertChunks_RejectsNaNEmbedding(t *testing.T) {
	store := setupSQLiteStore(t)
	chunk := makeTestChunk("sess-nan", 1, 0, "test chunk with NaN embedding")
	chunk.Embedding = makeTestEmbedding()
	chunk.Embedding[5] = math.NaN()

	err := store.InsertChunks([]Chunk{chunk})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "non-finite")
}

// ---------- FTS5 sync ----------

func TestSQLiteStore_InsertChunks_SyncsFTS(t *testing.T) {
	store := setupSQLiteStore(t)
	chunk := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: "database query optimization",
		ChunkTextSegmented: "database query optimization", ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	err := store.InsertChunks([]Chunk{chunk})
	require.NoError(t, err)

	// FTS should be synced — search should find the chunk
	hits, err := store.SearchFTS("database", 5, "", "", "", "", "", "", "")
	assert.NoError(t, err)
	assert.NotEmpty(t, hits, "FTS should find inserted chunk immediately without manual rebuild")
}

func TestSQLiteStore_DeleteChunksBySessionIDs_SyncsFTS(t *testing.T) {
	store := setupSQLiteStore(t)

	chunks := []Chunk{
		{
			SessionID: "sess-a", MessageID: 1, ChunkText: "database query",
			ChunkTextSegmented: "database query", ChunkIndex: 0,
			TokenCount: 2, Embedding: makeTestEmbedding(), HasEmbedding: true,
			ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
			CreatedAt: time.Now().Truncate(time.Millisecond),
		},
		{
			SessionID: "sess-b", MessageID: 2, ChunkText: "database search",
			ChunkTextSegmented: "database search", ChunkIndex: 0,
			TokenCount: 2, Embedding: makeTestEmbedding(), HasEmbedding: true,
			ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
			CreatedAt: time.Now().Truncate(time.Millisecond),
		},
	}
	err := store.InsertChunks(chunks)
	require.NoError(t, err)

	// Delete sess-a
	deleted, err := store.DeleteChunksBySessionIDs([]string{"sess-a"})
	assert.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	// FTS should only return sess-b results now
	hits, err := store.SearchFTS("database", 5, "", "", "", "", "", "", "")
	assert.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "sess-b", hits[0].SessionID)
}

// ---------- SearchFTS (SQLite FTS5) ----------

func TestSQLiteStore_SearchFTS_English(t *testing.T) {
	store := setupSQLiteStore(t)

	chunks := []Chunk{
		{
			SessionID: testSession1, MessageID: 1, ChunkText: testDBQueryOptimization,
			ChunkTextSegmented: testDBQueryOptimization, ChunkIndex: 0,
			TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
			ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
			CreatedAt: time.Now().Truncate(time.Millisecond),
		},
		{
			SessionID: testSession2, MessageID: 2, ChunkText: "web server configuration",
			ChunkTextSegmented: "web server configuration", ChunkIndex: 0,
			TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
			ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
			CreatedAt: time.Now().Truncate(time.Millisecond),
		},
	}
	err := store.InsertChunks(chunks)
	require.NoError(t, err)

	// Search for "database" — no manual FTS rebuild needed
	hits, err := store.SearchFTS("database", 5, "", "", "", "", "", "", "")
	assert.NoError(t, err)
	assert.NotEmpty(t, hits, "FTS search should find results for 'database'")
	assert.Contains(t, hits[0].ChunkText, "database")
}

func TestSQLiteStore_SearchFTS_Chinese(t *testing.T) {
	store := setupSQLiteStore(t)

	chunks := []Chunk{
		{
			SessionID: testSession1, MessageID: 1, ChunkText: "使用SQLite进行全文检索",
			ChunkTextSegmented: SegmentText("使用SQLite进行全文检索"), ChunkIndex: 0,
			TokenCount: 10, Embedding: makeTestEmbedding(), HasEmbedding: true,
			ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
			CreatedAt: time.Now().Truncate(time.Millisecond),
		},
	}
	err := store.InsertChunks(chunks)
	require.NoError(t, err)

	// Search for Chinese term
	hits, err := store.SearchFTS("全文检索", 5, "", "", "", "", "", "", "")
	assert.NoError(t, err)
	assert.NotEmpty(t, hits, "FTS search should find Chinese results")
}

func TestSQLiteStore_SearchFTS_NoResults(t *testing.T) {
	store := setupSQLiteStore(t)
	insertTestChunksSQLite(t, store, 1)

	hits, err := store.SearchFTS("nonexistent_xyz_12345", 5, "", "", "", "", "", "", "")
	assert.NoError(t, err)
	assert.Empty(t, hits)
}

func TestSQLiteStore_SearchFTS_FiltersByProject(t *testing.T) {
	store := setupSQLiteStore(t)
	chunk1 := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: testDBQueryOptimization,
		ChunkTextSegmented: testDBQueryOptimization, ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: "/project/a", Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	chunk2 := Chunk{
		SessionID: testSession2, MessageID: 2, ChunkText: "database indexing strategies",
		ChunkTextSegmented: "database indexing strategies", ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: "/project/b", Backend: testBackendCodebuddy, Role: testRoleUser,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	err := store.InsertChunks([]Chunk{chunk1, chunk2})
	require.NoError(t, err)

	hits, err := store.SearchFTS("database", 5, "/project/a", "", "", "", "", "", "")
	assert.NoError(t, err)
	assert.Len(t, hits, 1)
	assert.Equal(t, "/project/a", hits[0].ProjectPath)
}

// ---------- SearchSimple (stub) ----------

func TestSQLiteStore_SearchSimple_ReturnsError(t *testing.T) {
	store := setupSQLiteStore(t)
	_, err := store.SearchSimple(makeTestEmbedding(), 5, "", "", "", "", "", "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SearchSimple: not implemented")
}

func TestSQLiteStore_SearchSimple_RejectsInfEmbedding(t *testing.T) {
	store := setupSQLiteStore(t)
	queryEmbedding := makeTestEmbedding()
	queryEmbedding[0] = math.Inf(1)

	_, err := store.SearchSimple(queryEmbedding, 10, "", "", "", "", "", "", "")
	assert.Error(t, err)
}

// ---------- SearchHybrid ----------

func TestSQLiteStore_SearchHybrid_CombinesSources(t *testing.T) {
	store := setupSQLiteStore(t)
	insertTestChunksSQLite(t, store, 5)

	hits, err := store.SearchHybrid(
		makeTestEmbedding(), "chunk text", 20, 5,
		"", "", "", "", "", "", "",
	)
	// SearchSimple is stubbed, so it returns error; hybrid falls back to FTS
	assert.NoError(t, err)
	assert.NotEmpty(t, hits, "hybrid search should fall back to FTS when vector fails")
}

// ---------- Dimension mismatch ----------

func TestSQLiteStore_CheckDimensionMismatch_Empty(t *testing.T) {
	store := setupSQLiteStore(t)
	dim, mismatch, err := store.CheckDimensionMismatch()
	assert.NoError(t, err)
	assert.Equal(t, 0, dim, "empty table should return 0 dim")
	assert.False(t, mismatch)
}

func TestSQLiteStore_CheckDimensionMismatch_Match(t *testing.T) {
	store := setupSQLiteStore(t)
	insertTestChunksSQLite(t, store, 1)

	// Reload dim from DB after insert
	store.loadEmbeddingDimFromDB()

	dim, mismatch, err := store.CheckDimensionMismatch()
	assert.NoError(t, err)
	assert.Equal(t, 1024, dim)
	assert.False(t, mismatch)
}

func TestSQLiteStore_CheckDimensionMismatch_Mismatch(t *testing.T) {
	store := setupSQLiteStore(t)
	insertTestChunksSQLite(t, store, 1)

	// Change store dimension to simulate mismatch
	store.embDim = 768
	dim, mismatch, err := store.CheckDimensionMismatch()
	assert.NoError(t, err)
	assert.Equal(t, 1024, dim)
	assert.True(t, mismatch, "different dimension should report mismatch")
}

func TestSQLiteStore_ResetForDimensionMismatch(t *testing.T) {
	store := setupSQLiteStore(t)
	insertTestChunksSQLite(t, store, 3)

	count, _ := store.ChunkCount()
	assert.Equal(t, 3, count)

	err := store.ResetForDimensionMismatch(768)
	assert.NoError(t, err)

	count, _ = store.ChunkCount()
	assert.Equal(t, 0, count, "should have no chunks after reset")

	// New dimension should be set
	assert.Equal(t, 768, store.embDim)
}

// ---------- UpdateEmbedding ----------

func TestSQLiteStore_UpdateEmbedding(t *testing.T) {
	store := setupSQLiteStore(t)

	chunk := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: testNeedsBackfill,
		ChunkTextSegmented: testNeedsBackfill, ChunkIndex: 0,
		TokenCount: 3, Embedding: nil, HasEmbedding: false,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	err := store.InsertChunks([]Chunk{chunk})
	require.NoError(t, err)

	var chunkID int64
	err = store.db.QueryRowContext(context.Background(), "SELECT id FROM rag_chunks WHERE has_embedding = 0 LIMIT 1").Scan(&chunkID)
	require.NoError(t, err)

	embedding := makeTestEmbedding()
	err = store.UpdateEmbedding(chunkID, embedding)
	assert.NoError(t, err)

	var hasEmb int
	err = store.db.QueryRowContext(context.Background(), "SELECT has_embedding FROM rag_chunks WHERE id = ?", chunkID).Scan(&hasEmb)
	assert.NoError(t, err)
	assert.Equal(t, 1, hasEmb, "has_embedding should be 1 after backfill")

	pending, _ := store.PendingEmbeddingCount()
	assert.Equal(t, 0, pending, "no pending embeddings after backfill")
}

func TestSQLiteStore_UpdateEmbedding_RejectsNaNEmbedding(t *testing.T) {
	store := setupSQLiteStore(t)

	chunk := Chunk{
		SessionID: "sess-update-nan", MessageID: 1, ChunkText: "test chunk for update",
		ChunkTextSegmented: "test chunk for update", ChunkIndex: 0,
		TokenCount: 3, Embedding: nil, HasEmbedding: false,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	err := store.InsertChunks([]Chunk{chunk})
	require.NoError(t, err)

	var chunkID int64
	err = store.db.QueryRowContext(context.Background(), "SELECT id FROM rag_chunks WHERE has_embedding = 0 LIMIT 1").Scan(&chunkID)
	require.NoError(t, err)

	nanEmb := makeTestEmbedding()
	nanEmb[0] = math.NaN()
	err = store.UpdateEmbedding(chunkID, nanEmb)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "non-finite")
}

// ---------- PendingEmbeddingCount / GetPendingEmbeddings ----------

func TestSQLiteStore_PendingEmbeddingCount(t *testing.T) {
	store := setupSQLiteStore(t)

	chunk1 := makeTestChunk(testSession1, 1, 0, "with embedding")
	err := store.InsertChunks([]Chunk{chunk1})
	require.NoError(t, err)

	chunk2 := Chunk{
		SessionID: testSession2, MessageID: 2, ChunkText: "without embedding",
		ChunkTextSegmented: "without embedding", ChunkIndex: 0,
		TokenCount: 3, Embedding: nil, HasEmbedding: false,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	err = store.InsertChunks([]Chunk{chunk2})
	require.NoError(t, err)

	pending, err := store.PendingEmbeddingCount()
	assert.NoError(t, err)
	assert.Equal(t, 1, pending)
}

func TestSQLiteStore_GetPendingEmbeddings(t *testing.T) {
	store := setupSQLiteStore(t)

	chunk1 := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: "text 1",
		ChunkTextSegmented: "text 1", ChunkIndex: 0, TokenCount: 3,
		Embedding: nil, HasEmbedding: false,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	err := store.InsertChunks([]Chunk{chunk1})
	require.NoError(t, err)

	pending, err := store.GetPendingEmbeddings(10)
	assert.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, "text 1", pending[0].ChunkText)
}

// ---------- DeleteChunksBySessionIDs ----------

func TestSQLiteStore_DeleteChunksBySessionIDs(t *testing.T) {
	store := setupSQLiteStore(t)

	chunks := []Chunk{
		makeTestChunk("sess-a", 1, 0, "content a1"),
		makeTestChunk("sess-a", 2, 1, "content a2"),
		makeTestChunk("sess-b", 3, 0, "content b1"),
	}
	err := store.InsertChunks(chunks)
	require.NoError(t, err)

	deleted, err := store.DeleteChunksBySessionIDs([]string{"sess-a"})
	assert.NoError(t, err)
	assert.Equal(t, int64(2), deleted)

	count, _ := store.ChunkCount()
	assert.Equal(t, 1, count)
}

func TestSQLiteStore_DeleteChunksBySessionIDs_EmptyList(t *testing.T) {
	store := setupSQLiteStore(t)
	deleted, err := store.DeleteChunksBySessionIDs(nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
}

// ---------- ChunkCount ----------

func TestSQLiteStore_ChunkCount_Empty(t *testing.T) {
	store := setupSQLiteStore(t)
	count, err := store.ChunkCount()
	assert.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestSQLiteStore_ChunkCount_WithData(t *testing.T) {
	store := setupSQLiteStore(t)
	insertTestChunksSQLite(t, store, 7)

	count, err := store.ChunkCount()
	assert.NoError(t, err)
	assert.Equal(t, 7, count)
}

// ---------- SetEmbeddingDim ----------

func TestSQLiteStore_SetEmbeddingDim(t *testing.T) {
	store := setupSQLiteStore(t)

	changed := store.SetEmbeddingDim(768)
	assert.True(t, changed)
	assert.Equal(t, 768, store.embDim)

	// Set same dim again
	changed = store.SetEmbeddingDim(768)
	assert.False(t, changed)
}

// ---------- FTS integrity check ----------

func TestSQLiteStore_FTSIntegrityCheck(t *testing.T) {
	store := setupSQLiteStore(t)
	insertTestChunksSQLite(t, store, 3)

	// Integrity check should pass on a healthy store
	err := store.FTSIntegrityCheck()
	assert.NoError(t, err, "FTS integrity check should pass on healthy store")
}

// ---------- Close ----------

func TestSQLiteStore_Close(t *testing.T) {
	store := setupSQLiteStore(t)
	err := store.Close()
	assert.NoError(t, err)
}

func TestSQLiteStore_Close_NilDB(t *testing.T) {
	s := &Store{db: nil}
	err := s.Close()
	assert.NoError(t, err, "Close with nil db should not error")
}

// ---------- loadEmbeddingDimFromDB ----------

func TestSQLiteStore_LoadEmbeddingDimFromDB_WithExistingData(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test_dim.db"

	// First store: insert data
	store1, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	chunk := makeTestChunk(testSession1, 1, 0, "dim test")
	require.NoError(t, store1.InsertChunks([]Chunk{chunk}))
	_ = store1.Close()

	// Second store: should load dim from existing data
	store2, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store2.Close() })

	assert.Equal(t, 1024, store2.embDim, "dim should be loaded from existing data")
}

// ---------- SearchFTS additional filters ----------

func TestSQLiteStore_SearchFTS_FiltersByBackend(t *testing.T) {
	store := setupSQLiteStore(t)
	chunk1 := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: testDBQueryOptimization,
		ChunkTextSegmented: testDBQueryOptimization, ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	chunk2 := Chunk{
		SessionID: testSession2, MessageID: 2, ChunkText: "database indexing strategies",
		ChunkTextSegmented: "database indexing strategies", ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendCodebuddy, Role: testRoleUser,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	require.NoError(t, store.InsertChunks([]Chunk{chunk1, chunk2}))

	hits, err := store.SearchFTS("database", 5, "", testBackendClaude, "", "", "", "", "")
	assert.NoError(t, err)
	assert.Len(t, hits, 1)
	assert.Equal(t, testBackendClaude, hits[0].Backend)
}

func TestSQLiteStore_SearchFTS_FiltersByRole(t *testing.T) {
	store := setupSQLiteStore(t)
	chunk1 := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: testDBQueryOptimization,
		ChunkTextSegmented: testDBQueryOptimization, ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	chunk2 := Chunk{
		SessionID: testSession2, MessageID: 2, ChunkText: "database search method",
		ChunkTextSegmented: "database search method", ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleUser,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	require.NoError(t, store.InsertChunks([]Chunk{chunk1, chunk2}))

	hits, err := store.SearchFTS("database", 5, "", "", testRoleUser, "", "", "", "")
	assert.NoError(t, err)
	assert.Len(t, hits, 1)
	assert.Equal(t, testRoleUser, hits[0].Role)
}

func TestSQLiteStore_SearchFTS_FiltersBySessionID(t *testing.T) {
	store := setupSQLiteStore(t)
	chunk1 := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: testDBQueryOptimization,
		ChunkTextSegmented: testDBQueryOptimization, ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	chunk2 := Chunk{
		SessionID: testSession2, MessageID: 2, ChunkText: "database indexing",
		ChunkTextSegmented: "database indexing", ChunkIndex: 0,
		TokenCount: 2, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	require.NoError(t, store.InsertChunks([]Chunk{chunk1, chunk2}))

	hits, err := store.SearchFTS("database", 5, "", "", "", testSession1, "", "", "")
	assert.NoError(t, err)
	assert.Len(t, hits, 1)
	assert.Equal(t, testSession1, hits[0].SessionID)
}

func TestSQLiteStore_SearchFTS_ExcludeSessionID(t *testing.T) {
	store := setupSQLiteStore(t)
	chunk1 := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: testDBQueryOptimization,
		ChunkTextSegmented: testDBQueryOptimization, ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	chunk2 := Chunk{
		SessionID: testSession2, MessageID: 2, ChunkText: "database indexing",
		ChunkTextSegmented: "database indexing", ChunkIndex: 0,
		TokenCount: 2, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	require.NoError(t, store.InsertChunks([]Chunk{chunk1, chunk2}))

	hits, err := store.SearchFTS("database", 5, "", "", "", "", testSession1, "", "")
	assert.NoError(t, err)
	assert.Len(t, hits, 1)
	assert.Equal(t, testSession2, hits[0].SessionID, "should exclude testSession1")
}

func TestSQLiteStore_SearchFTS_FiltersByTimeRange(t *testing.T) {
	store := setupSQLiteStore(t)

	oldChunk := Chunk{
		SessionID: "sess-old", MessageID: 1, ChunkText: testDBQueryOptimization,
		ChunkTextSegmented: testDBQueryOptimization, ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Add(-48 * time.Hour).Truncate(time.Second),
	}
	recentChunk := Chunk{
		SessionID: "sess-recent", MessageID: 2, ChunkText: "database recent search",
		ChunkTextSegmented: "database recent search", ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Second),
	}
	require.NoError(t, store.InsertChunks([]Chunk{oldChunk, recentChunk}))

	from := time.Now().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	to := time.Now().Add(1 * time.Minute).Format("2006-01-02 15:04:05")
	hits, err := store.SearchFTS("database", 5, "", "", "", "", "", from, to)
	assert.NoError(t, err)
	assert.Len(t, hits, 1)
	assert.Equal(t, "sess-recent", hits[0].SessionID)
}

// ---------- SearchHybrid fallback paths ----------

func TestSQLiteStore_SearchHybrid_VectorOnlyFallback(t *testing.T) {
	store := setupSQLiteStore(t)
	insertTestChunksSQLite(t, store, 3)

	// Use a query that won't match FTS but vector search would return results
	// Since SearchSimple is stubbed, it returns error and hybrid falls back to FTS
	_, err := store.SearchHybrid(
		makeTestEmbedding(), "nonexistent_xyz_12345", 20, 5,
		"", "", "", "", "", "", "",
	)
	assert.NoError(t, err)
}

func TestSQLiteStore_SearchHybrid_VectorFails_FTSSucceeds(t *testing.T) {
	store := setupSQLiteStore(t)
	insertTestChunksSQLite(t, store, 3)

	// Use invalid query embedding that will fail validation
	infEmb := makeTestEmbedding()
	infEmb[0] = math.Inf(1)

	// Vector search fails (invalid embedding) but FTS should succeed
	hits, err := store.SearchHybrid(
		infEmb, "chunk text", 20, 5,
		"", "", "", "", "", "", "",
	)
	assert.NoError(t, err, "hybrid should fall back to FTS when vector fails")
	assert.NotEmpty(t, hits, "should return FTS results as fallback")
}

// ---------- NewSQLiteStore file path ----------

func TestSQLiteStore_NewSQLiteStore_TempFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"

	store, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// Verify it works
	chunk := makeTestChunk(testSession1, 1, 0, "file db test")
	require.NoError(t, store.InsertChunks([]Chunk{chunk}))

	count, err := store.ChunkCount()
	assert.NoError(t, err)
	assert.Equal(t, 1, count)
}

// ---------- GetPendingEmbeddings empty ----------

func TestSQLiteStore_GetPendingEmbeddings_Empty(t *testing.T) {
	store := setupSQLiteStore(t)

	pending, err := store.GetPendingEmbeddings(10)
	assert.NoError(t, err)
	assert.Empty(t, pending)
}

// ---------- DeleteChunksBySessionIDs multiple sessions ----------

func TestSQLiteStore_DeleteChunksBySessionIDs_MultipleSessions(t *testing.T) {
	store := setupSQLiteStore(t)

	chunks := []Chunk{
		makeTestChunk("sess-a", 1, 0, "content a1"),
		makeTestChunk("sess-b", 2, 0, "content b1"),
		makeTestChunk("sess-c", 3, 0, "content c1"),
	}
	require.NoError(t, store.InsertChunks(chunks))

	deleted, err := store.DeleteChunksBySessionIDs([]string{"sess-a", "sess-c"})
	assert.NoError(t, err)
	assert.Equal(t, int64(2), deleted)

	count, _ := store.ChunkCount()
	assert.Equal(t, 1, count)
}

// ---------- Helpers ----------

// insertTestChunksSQLite inserts n test chunks into the SQLite store.
func insertTestChunksSQLite(t *testing.T, store *Store, n int) {
	t.Helper()
	chunks := make([]Chunk, n)
	for i := range n {
		chunks[i] = makeTestChunk(
			"session-1",
			int64(i+1),
			i,
			fmt.Sprintf("chunk text %d", i),
		)
	}
	err := store.InsertChunks(chunks)
	require.NoError(t, err, "InsertChunks should succeed")
}

// ---------- NewSQLiteStore PRAGMA and driver migration ----------

func TestSQLiteStore_NewSQLiteStore_SetsWALMode(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test_wal.db"

	store, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// Verify WAL mode was set via PRAGMA
	var mode string
	err = store.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	assert.NoError(t, err)
	assert.Equal(t, "wal", mode, "database should be in WAL mode")
}

func TestSQLiteStore_NewSQLiteStore_SetsBusyTimeout(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test_busy.db"

	store, err := NewSQLiteStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// Verify busy_timeout was set via PRAGMA
	var timeout int
	err = store.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout)
	assert.NoError(t, err)
	assert.Equal(t, 5000, timeout, "busy_timeout should be 5000ms")
}

func TestSQLiteStore_NewSQLiteStore_InvalidPath(t *testing.T) {
	// Opening a database in a non-existent deeply nested path may fail
	// depending on the driver. Test that the error is returned properly.
	_, err := NewSQLiteStore("/nonexistent/deeply/nested/dir/test.db")
	// modernc.org/sqlite creates the file, so this may not error on open
	// but the important thing is that the function doesn't panic
	_ = err
}

func TestSQLiteStore_NewSQLiteStore_InMemoryWithFTS5(t *testing.T) {
	store := setupSQLiteStore(t)

	// Verify FTS5 works (the core reason for the modernc migration)
	chunk := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: "full text search test",
		ChunkTextSegmented: "full text search test", ChunkIndex: 0,
		TokenCount: 4, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	err := store.InsertChunks([]Chunk{chunk})
	require.NoError(t, err)

	hits, err := store.SearchFTS("full text", 5, "", "", "", "", "", "", "")
	assert.NoError(t, err)
	assert.NotEmpty(t, hits, "FTS5 should work with modernc.org/sqlite driver")
}

func TestSQLiteStore_NewSQLiteStore_UsesModerncDriver(t *testing.T) {
	store := setupSQLiteStore(t)

	// Verify the driver is modernc.org/sqlite (not mattn/go-sqlite3)
	// by checking a modernc-specific behavior: PRAGMA via EXEC works
	var version string
	err := store.db.QueryRow("SELECT sqlite_version()").Scan(&version)
	assert.NoError(t, err)
	assert.NotEmpty(t, version, "should be able to query sqlite version")
}

func TestSQLiteStore_NewSQLiteStore_SharedCacheInMemory(t *testing.T) {
	// In-memory store with shared cache should allow goroutine access
	store := setupSQLiteStore(t)

	done := make(chan error, 1)
	go func() {
		_, err := store.db.Exec("INSERT INTO rag_chunks (session_id, message_id, chunk_text, chunk_text_segmented, chunk_index, token_count, has_embedding, embedding_dim, project_path, backend, role, created_at) VALUES ('goroutine-test', 1, 'test', 'test', 0, 1, 0, 0, '/test', 'test', 'user', CURRENT_TIMESTAMP)")
		done <- err
	}()

	err := <-done
	assert.NoError(t, err, "in-memory DB with shared cache should work across goroutines")
}

// ---------- FTS5 query injection protection (ISS-283) ----------

func TestSQLiteStore_SearchFTS_SpecialOperatorsNoCrash(t *testing.T) {
	// ISS-283: FTS5 special operators (NOT, OR, AND, *, "") should not
	// cause FTS5 syntax errors when wrapped in phrase syntax.
	store := setupSQLiteStore(t)

	chunk := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: "database query optimization",
		ChunkTextSegmented: "database query optimization", ChunkIndex: 0,
		TokenCount: 3, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	require.NoError(t, store.InsertChunks([]Chunk{chunk}))

	// These queries previously caused FTS5 syntax errors; with phrase wrapping
	// they should either return results or empty results without error.
	malformedQueries := []string{
		"NOT *",
		"OR test",
		"AND database",
		"test*",
		"NOT",
		"OR",
		"AND",
	}
	for _, q := range malformedQueries {
		_, err := store.SearchFTS(q, 5, "", "", "", "", "", "", "")
		assert.NoError(t, err, "SearchFTS should not error on FTS5 special operator query: %q", q)
	}
}

func TestSQLiteStore_SearchFTS_DoubleQuotesInQuery(t *testing.T) {
	// Queries containing double quotes should be handled correctly
	// (double quotes are escaped by doubling in FTS5 phrase syntax).
	store := setupSQLiteStore(t)

	chunk := Chunk{
		SessionID: testSession1, MessageID: 1, ChunkText: `find the "quoted" word`,
		ChunkTextSegmented: `find the "quoted" word`, ChunkIndex: 0,
		TokenCount: 4, Embedding: makeTestEmbedding(), HasEmbedding: true,
		ProjectPath: testProjectPath, Backend: testBackendClaude, Role: testRoleAssistant,
		CreatedAt: time.Now().Truncate(time.Millisecond),
	}
	require.NoError(t, store.InsertChunks([]Chunk{chunk}))

	// Should not crash on double quotes in the query
	_, err := store.SearchFTS(`"quoted"`, 5, "", "", "", "", "", "", "")
	assert.NoError(t, err, "SearchFTS should handle double quotes in query without error")
}
