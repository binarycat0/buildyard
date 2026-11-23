package org.slf4j.impl;

import org.slf4j.ILoggerFactory;
import org.slf4j.Logger;

/** Compile-only stand-in for mvnd 1.x's class of the same name. */
public class MvndLoggerFactory implements ILoggerFactory {
    @Override
    public Logger getLogger(String name) {
        return null;
    }

    public void reconfigure() {
    }
}
