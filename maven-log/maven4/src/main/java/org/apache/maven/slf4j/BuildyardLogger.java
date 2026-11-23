package org.apache.maven.slf4j;

import org.slf4j.Marker;
import org.slf4j.event.Level;
import org.slf4j.helpers.MessageFormatter;

import dev.buildyard.log.JsonSink;

/** Maven 4's own logger (the console is unchanged) that also writes each call as JSON. */
public class BuildyardLogger extends MavenFailOnSeverityLogger {
    BuildyardLogger(String name, DefaultLogLevelRecorder recorder) {
        super(name, recorder);
    }

    @Override
    protected void handleNormalizedLoggingCall(Level level, Marker marker, String msg, Object[] args, Throwable t) {
        super.handleNormalizedLoggingCall(level, marker, msg, args, t);
        JsonSink.write(level.name(), getName(), MessageFormatter.basicArrayFormat(msg, args), t);
    }
}
