package toolconfig

import (
	"fmt"
	"html"
	"strings"
)

const settingsTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<settings xmlns="http://maven.apache.org/SETTINGS/1.2.0"
          xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
          xsi:schemaLocation="http://maven.apache.org/SETTINGS/1.2.0 https://maven.apache.org/xsd/settings-1.2.0.xsd">
  <!-- <localRepository>${user.home}/.m2/repository</localRepository> -->
  <mirrors>
  </mirrors>
  <servers>
  </servers>
  <profiles>
  </profiles>
</settings>
`

const toolchainsHeader = `<?xml version="1.0" encoding="UTF-8"?>
<toolchains xmlns="http://maven.apache.org/TOOLCHAINS/1.1.0"
            xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
            xsi:schemaLocation="http://maven.apache.org/TOOLCHAINS/1.1.0 https://maven.apache.org/xsd/toolchains-1.1.0.xsd">
`

const toolchainsFooter = "</toolchains>\n"

// ToolchainJDK is a JDK to list in a generated toolchains.xml.
type ToolchainJDK struct {
	Home    string
	Version string // full version, e.g. 21.0.8
	Major   int
	Vendor  string
}

// Toolchains renders a toolchains.xml with one jdk toolchain per JDK. Each provides
// its full version (so ranges like [21,22) match) and vendor.
func Toolchains(jdks []ToolchainJDK) string {
	var b strings.Builder
	b.WriteString(toolchainsHeader)
	for _, j := range jdks {
		version := j.Version
		if version == "" && j.Major > 0 {
			version = fmt.Sprint(j.Major)
		}
		b.WriteString("  <toolchain>\n    <type>jdk</type>\n    <provides>\n")
		fmt.Fprintf(&b, "      <version>%s</version>\n", html.EscapeString(version))
		if j.Vendor != "" {
			fmt.Fprintf(&b, "      <vendor>%s</vendor>\n", html.EscapeString(j.Vendor))
		}
		b.WriteString("    </provides>\n    <configuration>\n")
		fmt.Fprintf(&b, "      <jdkHome>%s</jdkHome>\n", html.EscapeString(j.Home))
		b.WriteString("    </configuration>\n  </toolchain>\n")
	}
	b.WriteString(toolchainsFooter)
	return b.String()
}
