package dev.buildyard.log;

import org.slf4j.ILoggerFactory;

/** Entry point for the binding in org.slf4j.impl. */
public final class Buildyard {
    private static boolean captured;

    private Buildyard() {
    }

    /** Wraps Maven's factory; the first call also starts recording direct System.out/err lines. */
    public static synchronized ILoggerFactory factory(ILoggerFactory maven) {
        if (!captured) {
            captured = true;
            StdoutCapture.ensure(); // before Maven's logger caches System.out
        }
        return new TeeLoggerFactory(maven);
    }
}
