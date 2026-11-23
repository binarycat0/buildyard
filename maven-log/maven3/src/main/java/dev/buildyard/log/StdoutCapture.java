package dev.buildyard.log;

import java.io.ByteArrayOutputStream;
import java.io.OutputStream;
import java.io.PrintStream;
import java.nio.charset.Charset;

/**
 * Maven 3 lets plugins print to System.out and System.err directly (forked test output,
 * tools run by a plugin). This passes everything through unchanged and also records each
 * such line as JSON, at INFO from the "stdout" or "stderr" logger. Lines the loggers print
 * themselves are not recorded twice. (Maven 4 and mvnd route these lines to their logger.)
 */
final class StdoutCapture extends OutputStream {
    private static final ThreadLocal<int[]> IN_LOGGER = new ThreadLocal<int[]>() {
        @Override
        protected int[] initialValue() {
            return new int[1];
        }
    };

    private final OutputStream target;
    private final String logger;
    private final ThreadLocal<ByteArrayOutputStream> line = new ThreadLocal<ByteArrayOutputStream>() {
        @Override
        protected ByteArrayOutputStream initialValue() {
            return new ByteArrayOutputStream(128);
        }
    };

    private StdoutCapture(OutputStream target, String logger) {
        this.target = target;
        this.logger = logger;
    }

    private static volatile PrintStream out;
    private static volatile PrintStream err;

    /**
     * Wraps System.out and System.err, again whenever something replaced them since (Maven
     * swaps in its ANSI console streams after logging starts). Cheap when nothing changed.
     */
    static void ensure() {
        if (System.out == out && System.err == err) {
            return;
        }
        synchronized (StdoutCapture.class) {
            if (System.out != out) {
                out = wrap(System.out, "stdout");
                System.setOut(out);
            }
            if (System.err != err) {
                err = wrap(System.err, "stderr");
                System.setErr(err);
            }
        }
    }

    private static PrintStream wrap(PrintStream ps, String logger) {
        return new PrintStream(new StdoutCapture(ps, logger), true);
    }

    static void enterLogger() {
        IN_LOGGER.get()[0]++;
    }

    static void exitLogger() {
        IN_LOGGER.get()[0]--;
    }

    // A write records its bytes only when no logger call and no other capture is under
    // way on this thread: a stream that wraps an older capture must not record twice.
    @Override
    public void write(int b) throws java.io.IOException {
        int[] depth = IN_LOGGER.get();
        boolean record = depth[0] == 0;
        depth[0]++;
        try {
            target.write(b);
        } finally {
            depth[0]--;
        }
        if (record) {
            take(b);
        }
    }

    @Override
    public void write(byte[] b, int off, int len) throws java.io.IOException {
        int[] depth = IN_LOGGER.get();
        boolean record = depth[0] == 0;
        depth[0]++;
        try {
            target.write(b, off, len);
        } finally {
            depth[0]--;
        }
        if (record) {
            for (int i = off; i < off + len; i++) {
                take(b[i]);
            }
        }
    }

    @Override
    public void flush() throws java.io.IOException {
        target.flush();
    }

    private void take(int b) {
        ByteArrayOutputStream buf = line.get();
        if (b == '\n') {
            byte[] bytes = buf.toByteArray();
            buf.reset();
            int n = bytes.length;
            if (n > 0 && bytes[n - 1] == '\r') {
                n--;
            }
            JsonSink.write("INFO", logger, new String(bytes, 0, n, Charset.defaultCharset()), null);
        } else if (buf.size() < 1 << 20) { // a runaway line without newlines must not eat the heap
            buf.write(b);
        }
    }
}
