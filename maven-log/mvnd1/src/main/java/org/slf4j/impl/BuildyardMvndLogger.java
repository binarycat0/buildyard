package org.slf4j.impl;

import dev.buildyard.log.JsonSink;

/** mvnd 1.x's own logger (the client output is unchanged) that also writes each call as JSON. */
public class BuildyardMvndLogger extends MvndSimpleLogger {
    private static final long serialVersionUID = 1L;

    BuildyardMvndLogger(String name) {
        super(name);
    }

    @Override
    protected void doLog(int level, String msg, Throwable t) {
        super.doLog(level, msg, t);
        JsonSink.write(levelName(level), getName(), msg, t);
    }

    // SLF4J's LocationAwareLogger levels, which mvnd uses.
    private static String levelName(int level) {
        if (level >= 40) {
            return "ERROR";
        }
        if (level >= 30) {
            return "WARN";
        }
        if (level >= 20) {
            return "INFO";
        }
        return level >= 10 ? "DEBUG" : "TRACE";
    }
}
