// Classifies Maven output lines for the log viewer's filters.
// Each line gets a level (error, warn, info, debug, plain) and an "important" flag:
// errors, warnings, module boundaries, test summaries, the Reactor Summary and the result.

const levelRe = /^(?:\[[^\]]*\] )?\[(ERROR|WARNING|WARN|INFO|DEBUG)\] ?/;
const importantRe = [
  /^-+< \S+ >-+$/, // module boundary: ----< group:artifact >----
  /^Building .+ \[\d+\/\d+\]$/, // Building Name 1.0 [12/959]
  /^Tests run: \d+/,
  /^Reactor Summary/,
  /^BUILD (SUCCESS|FAILURE)/,
  /^Total time:/,
  /^Failed to execute goal/,
  /^(COMPILATION ERROR|Compilation failure)/,
  /^-{5,} .+ -{5,}$/, // mvnd: ----- Last 200 lines from daemon log file ... -----
];

// Lines without a level that still report a failure: a JVM or build-tool crash (mvnd's
// daemon dying prints only this, with no [ERROR] line at all). Its "Caused by:" and
// stack lines follow without a level, so they continue the error.
const crashRe = [
  /^Exception in thread "/,
  /daemon may have crashed/i,
  /^# A fatal error has been detected by the Java Runtime/,
  /^java\.lang\.OutOfMemoryError/,
];

export class LineClassifier {
  constructor() {
    this.prev = 'info'; // a line without a level continues the previous error/warning (stack traces, javac details)
    this.inSummary = false;
    this.crashed = false; // a build-tool crash was seen: its later stack lines are errors
  }

  classify(text) {
    const m = levelRe.exec(text);
    let level;
    let msg = text;
    if (m) {
      level = { ERROR: 'error', WARNING: 'warn', WARN: 'warn', INFO: 'info', DEBUG: 'debug' }[m[1]];
      msg = text.slice(m[0].length);
      this.prev = level;
    } else if (crashRe.some((re) => re.test(text))) {
      level = 'error';
      this.prev = level; // the stack trace below continues it
      this.crashed = true;
    } else if (this.crashed && /^(?:\s+at |\s*\.\.\. \d+ more|Caused by: )/.test(text)) {
      level = 'error'; // the crash's stack trace, also after mvnd's daemon-log dump between
      this.prev = level;
    } else if (/^-{5,} .+ -{5,}$/.test(text.trim())) {
      level = 'plain'; // a banner (mvnd's daemon log tail) ends a stack trace
      this.prev = level;
    } else {
      level = this.prev === 'error' || this.prev === 'warn' ? this.prev : 'plain';
    }
    const body = msg.trim();
    if (/^Reactor Summary/.test(body)) this.inSummary = true;
    let important = level === 'error' || level === 'warn' || this.inSummary || importantRe.some((re) => re.test(body));
    if (/^BUILD (SUCCESS|FAILURE)/.test(body)) this.inSummary = false;
    if (m && body === '' ) important = false;
    const ok = /^BUILD SUCCESS/.test(body);
    const failure = /^BUILD FAILURE/.test(body);
    return { level: failure ? 'error' : level, important, ok };
  }
}

// Which lines each filter shows.
export const filters = {
  all: () => true,
  important: (c) => c.important,
  warn: (c) => c.level === 'error' || c.level === 'warn',
  error: (c) => c.level === 'error',
};
