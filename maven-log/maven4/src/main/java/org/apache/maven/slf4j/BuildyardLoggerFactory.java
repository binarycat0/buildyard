package org.apache.maven.slf4j;

import org.slf4j.Logger;

/**
 * Maven 4 logger factory. Maven's SLF4J provider looks up MavenLoggerFactory with a
 * ServiceLoader and takes the first one: this jar comes first on the classpath.
 */
public class BuildyardLoggerFactory extends MavenLoggerFactory {
    @Override
    protected Logger getNewLoggingInstance(String name) {
        return new BuildyardLogger(name, logLevelRecorder);
    }
}
