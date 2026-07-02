package com.clawbench.app;

import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import java.io.File;

import static org.junit.Assert.*;

/**
 * Unit tests for SharedCacheUtils (Share Out shared cache directory logic).
 */
public class MainActivityShareOutTest {

    private File tempCacheDir;

    @Before
    public void setUp() {
        tempCacheDir = new File(System.getProperty("java.io.tmpdir"),
                "clawbench-test-shared-" + System.currentTimeMillis());
        tempCacheDir.mkdirs();
    }

    @After
    public void tearDown() {
        // Clean up
        File sharedDir = new File(tempCacheDir, SharedCacheUtils.SHARED_DIR_NAME);
        if (sharedDir.exists()) {
            for (File f : sharedDir.listFiles()) f.delete();
            sharedDir.delete();
        }
        tempCacheDir.delete();
    }

    @Test
    public void getSharedCacheDir_createsDirectoryUnderCacheDir() {
        File result = SharedCacheUtils.getSharedCacheDir(tempCacheDir);
        assertTrue(result.exists());
        assertTrue(result.isDirectory());
        assertEquals(new File(tempCacheDir, "shared"), result);
    }

    @Test
    public void getSharedCacheDir_returnsExistingDirectory() {
        File first = SharedCacheUtils.getSharedCacheDir(tempCacheDir);
        File second = SharedCacheUtils.getSharedCacheDir(tempCacheDir);
        assertEquals(first, second);
        assertTrue(second.exists());
    }

    @Test
    public void cleanupSharedCacheDir_deletesFiles() throws Exception {
        File sharedDir = SharedCacheUtils.getSharedCacheDir(tempCacheDir);
        new File(sharedDir, "test1.png").createNewFile();
        new File(sharedDir, "test2.jpg").createNewFile();
        assertEquals(2, sharedDir.listFiles().length);

        int deleted = SharedCacheUtils.cleanupSharedCacheDir(tempCacheDir);
        assertEquals(2, deleted);
        assertEquals(0, sharedDir.listFiles().length);
    }

    @Test
    public void cleanupSharedCacheDir_returnsZeroWhenEmpty() {
        SharedCacheUtils.getSharedCacheDir(tempCacheDir);
        int deleted = SharedCacheUtils.cleanupSharedCacheDir(tempCacheDir);
        assertEquals(0, deleted);
    }

    @Test
    public void cleanupSharedCacheDir_handlesNonExistentDir() {
        File nonExistent = new File(System.getProperty("java.io.tmpdir"),
                "clawbench-nonexistent-" + System.currentTimeMillis());
        // Should not throw
        int deleted = SharedCacheUtils.cleanupSharedCacheDir(nonExistent);
        assertEquals(0, deleted);
        nonExistent.delete();
    }
}
