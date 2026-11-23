package org.slf4j.impl;

import org.slf4j.ILoggerFactory;
import org.slf4j.spi.LoggerFactoryBinder;

import dev.buildyard.log.Buildyard;

/**
 * SLF4J 1.7 binding. It comes before Maven's own binding on the classpath, so SLF4J picks
 * it; it hands out Maven's loggers wrapped to also write JSON.
 */
public final class StaticLoggerBinder implements LoggerFactoryBinder {
    public static String REQUESTED_API_VERSION = "1.7.36"; // not final: SLF4J reads it at runtime

    private static final StaticLoggerBinder SINGLETON = new StaticLoggerBinder();

    public static StaticLoggerBinder getSingleton() {
        return SINGLETON;
    }

    private final ILoggerFactory factory = Buildyard.factory(new MavenSimpleLoggerFactory());

    @Override
    public ILoggerFactory getLoggerFactory() {
        return factory;
    }

    @Override
    public String getLoggerFactoryClassStr() {
        return factory.getClass().getName();
    }
}
