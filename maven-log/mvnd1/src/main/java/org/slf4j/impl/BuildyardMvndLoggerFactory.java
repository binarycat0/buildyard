package org.slf4j.impl;

import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ConcurrentMap;

import org.slf4j.Logger;

/**
 * mvnd 1.x logger factory. mvnd's level setup casts the factory to MvndLoggerFactory and
 * its loggers to MvndSimpleLogger, so both are subclassed. The daemon's own loggers stay
 * mvnd's: they write the daemon log, and wrapping them feeds the build output back into itself.
 */
public class BuildyardMvndLoggerFactory extends MvndLoggerFactory {
    private final ConcurrentMap<String, BuildyardMvndLogger> loggers = new ConcurrentHashMap<>();

    @Override
    public Logger getLogger(String name) {
        if (name.startsWith("org.mvndaemon.mvnd.daemon")) {
            return super.getLogger(name);
        }
        return loggers.computeIfAbsent(name, BuildyardMvndLogger::new);
    }

    @Override
    public void reconfigure() {
        super.reconfigure();
        loggers.values().forEach(l -> l.configure(MvndSimpleLogger.CONFIG_PARAMS.defaultLogLevel));
    }
}
