package org.slf4j.impl;

import org.slf4j.ILoggerFactory;
import org.slf4j.spi.LoggerFactoryBinder;

/** SLF4J 1.7 binding for the mvnd 1.x daemon; it comes before mvnd's own on the classpath. */
public final class StaticLoggerBinder implements LoggerFactoryBinder {
    public static String REQUESTED_API_VERSION = "1.7.36"; // not final: SLF4J reads it at runtime

    private static final StaticLoggerBinder SINGLETON = new StaticLoggerBinder();

    public static StaticLoggerBinder getSingleton() {
        return SINGLETON;
    }

    private final ILoggerFactory factory = new BuildyardMvndLoggerFactory();

    @Override
    public ILoggerFactory getLoggerFactory() {
        return factory;
    }

    @Override
    public String getLoggerFactoryClassStr() {
        return BuildyardMvndLoggerFactory.class.getName();
    }
}
