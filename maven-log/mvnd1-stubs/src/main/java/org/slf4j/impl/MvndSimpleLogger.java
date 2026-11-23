package org.slf4j.impl;

import org.slf4j.helpers.NOPLogger;

/** Compile-only stand-in for mvnd 1.x's class of the same name. */
public class MvndSimpleLogger extends NOPLogger {
    static final SimpleLoggerConfiguration CONFIG_PARAMS = new SimpleLoggerConfiguration();

    MvndSimpleLogger(String name) {
    }

    protected void doLog(int level, String msg, Throwable t) {
    }

    public void configure(int defaultLevel) {
    }
}
