# Спайк фазы 0: сборка Dremio через Maven

Дата: 2026-11-17. Объект: монорепозиторий Dremio (`dremio-global/dremio`), HEAD `2b42a56` (до `git pull`: `f139f13`).
Цель — понять, как собирать Dremio без тестов, нативных библиотек и UI, как получать список модулей и как читать вывод Maven. Полностью зелёную сборку на этой машине не получили; для фазы 1 это не блокер.

Фикстуры логов: `testdata/maven-logs/` (см. раздел 8). Полные логи остались вне репозитория, в `~/buildyard-spike/`.

## 1. Окружение

| | |
|---|---|
| ОС | macOS 27.0.1, arm64, 16 ядер, 48 ГБ |
| JDK | OpenJDK 21.0.8 arm64 (Homebrew, через jenv) |
| `./mvnw` | Maven **3.9.16** (wrapper 3.3.4) — использовать его |
| `mvn` в PATH | Maven 3.9.11 — не использовать |
| `mvnd` | не установлен |
| `.mvn/maven.config` | `-Drevision=28.0.0-SNAPSHOT` (CI-friendly версия, меняется при `git pull`) |
| `.mvn/jvm.config` | `-Xms1024m -Xss2048k -XX:+UseParallelGC` + `--add-exports/--add-opens jdk.compiler/...` |
| `.mvn/extensions.xml` | `maven-profiler` (включается через `-Dprofile`), `maven-notifier`, `opentelemetry-maven-extension` (выключено) |

**JDK.** Сборку ведёт один JDK, разделения модулей на 17 и 21 нет. Enforcer требует JDK `[21.0.8,22)` и Maven `[3.9.7,4)`. До коммита `3f5d0f7` (22.10.2026) стоял `maven.compiler.release=17`, теперь 21. Toolchains (`~/.m2/toolchains.xml`, JDK 11/17/21) нужны только для тестов (`dremio.test-jdk.version`). Temurin 21.0.7 как основной JDK не прошёл бы enforcer.

## 2. Команда сборки

Источник: `dremio-global/AGENTS.md`, `dremio/dremio2-shortcuts.sh` (функция `drci`) и `README.md`.

```bash
cd dremio-global/dremio
ulimit -n $(sysctl -n kern.maxfilesperproc)
./mvnw -B clean install -DskipTests -Dlicense.skip -Dmaven.javadoc.skip=true \
  -Ddremio.no-native=true -Ddremio.no-ui -T 1.5C \
  -Dmaven.repo.local=/Users/<user>/dremio-global/.m2repo
```

Особенности:
- **`.m2repo`**: локальный репозиторий воркспейса, а не `~/.m2`. В `~/.m2` лежат устаревшие SNAPSHOT-ы.
- `ulimit -n` до максимума и `MAVEN_OPTS=-XX:-MaxFDLimit` (`~/.mavenrc`).
- `-T 1.5C` — как в CI (Jenkinsfile: `install -T 1.5C -DskipTests`).
- `clean` обязателен: без него устаревшие `target/` дают ложные ошибки (раздел 6).
- В shell-скриптах передавать команду массивом: в zsh `$CMD` из строки не разбивается на слова.

## 3. Флаги пропуска

| Что | Флаг | Как работает |
|---|---|---|
| Тесты | `-DskipTests` | тесты компилируются, но не запускаются (включая Go и pytest в daas) |
| Нативные библиотеки | `-Ddremio.no-native=true` | значение `=true` обязательно: профиль `no-native` в `joust-cpp`/`tracker-cpp` активен при `!= true` и включает cmake |
| UI | `-Ddremio.no-ui` | ставит `skip.uiclean/uibundle/installnodepnpm/pnpm`; 19 UI-модулей остаются в реакторе, но `frontend-maven-plugin` пишет «Skipping execution» |
| Lint | `-Ddremio.no-lint` | отключает enforcer, spotless, checkstyle, license, forbiddenapis, prettier, errorprone. В `drci` lint **включён** |
| Лицензии | `-Dlicense.skip` | |
| Javadoc | `-Dmaven.javadoc.skip=true` | |
| Без Cloud | `-Ddremio.no-daas-build` или `'-P!daas-build'` | убирает `daas/` |
| Только OSS | `-Ddremio.oss-only=true` | убирает extras/community/enterprise/daas |
| Go SDK | `-Dmvn.golang.get.skip=true` | так делает CI |
| Дистрибутив/Docker/RPM | выключены по умолчанию | профили `rpm`, `docker`, `daas-docker-*` только через явный `-P` |

## 4. Обнаружение модулей

| Способ | Время | Модулей |
|---|---|---|
| **`./mvnw -B -o help:active-profiles` + флаги** | **5.5 с** | 952 + активные профили каждого |
| `./mvnw -B -o help:all-profiles` | 5.5 с | все объявленные профили |
| `./mvnw -B validate` + флаги | 442 с | 952 (запускает плагины validate) |
| `exec:exec -Dexec.executable=echo` | падает | резолвит зависимости и в офлайне падает; к тому же офлайн не работает короткий префикс `exec:` |

Рекомендация: `help:active-profiles`. Ему нужен `maven-help-plugin` в локальном репозитории, иначе нужна сеть.
Модули по умолчанию: OSS + Community + Enterprise + DaaS. Корневой pom → `oss`; `oss/pom.xml`: профиль `with-extras-module` (`dremio.oss-only != true`) → `extras` (→ `community`, `enterprise`, `openapi`); `extras/pom.xml`: профиль `daas-build` → `daas`.

## 5. Вывод Maven при `-T`

- Строки разных модулей перемешаны.
- `-Dorg.slf4j.simpleLogger.showThreadName=true` → префикс `[mvn-builder-<artifactId>]` у каждой строки модуля, `[main]` — уровень реактора. Этого достаточно для атрибуции вывода.
- Работают все `-Dorg.slf4j.simpleLogger.*` (Maven 3.9.16, `maven-slf4j-provider`): `showDateTime`, `dateTimeFormat=HH:mm:ss.SSS`, `log.<logger>=<level>`. `...log.org.apache.maven.cli.event.ExecutionEventLogger=warn` убирает баннеры.
- Reactor Summary: у длинных имён модулей нет точек-заполнителей (`<name> FAILURE [ 18.775 s]`); время в форматах `NN.NNN s` и `MM:SS min`; есть строки `SKIPPED` без времени.
- Время модуля при `-T` включает ожидание CPU (сумма времён модулей 36077 с при 1736 с по стене). Для «медленных модулей» нужны timestamp-ы строк или `-Dprofile`.
- Формат `mvnd -B` не проверен: mvnd не установлен.

## 6. Найденные проблемы (классы падений для Buildyard)

1. **x86-плагин grpc в Quarkus-модулях.** Quarkus 3.27.x (BOM) фиксирует `io.grpc:protoc-gen-grpc-java` 1.69.1; для `osx-aarch_64` эта версия — x86_64-бинарь (универсальные появились с 1.77). Сейчас на машине x86-код не запускается, а 22.09.2026 (до установки macOS 27.0.1 26.10.2026) тот же бинарь работал. Падают 4 модуля: `Dremio Catalog Optimization - Sharable Test Code`, `Polaris Server - Sharable Test Code`, `Polaris Server - Common gRPC`, `DaaS Comics Saga - Deployment`. Симптом: `program not found or is not executable` в `quarkus-maven-plugin:generate-code`. Через свойства Quarkus можно подменить только `protoc` (`quarkus.grpc.protoc-path`), для grpc-плагина свойства нет.
2. **Устаревший `target/` без `clean`.**
   - forbiddenapis падает в `Common - Core` на `UrlUtils.class`, исходника которого уже нет.
   - Ошибка компиляции в `ee-sabot-kernel`: не совпадает сигнатура `PrivilegeService.getOwnerIds`.
   - Buildyard должен уметь чистить `target/` точечно — только у упавших модулей, не трогая собранный дистрибутив.
3. **Ловушка `-rf` при `-T`.** SKIPPED-модули могут стоять в порядке реактора раньше первого упавшего. `-rf :<failed>` их не пересобирает и берёт старые jar из локального репозитория → ложные ошибки компиляции. Правильно пересобирать FAILURE+SKIPPED прошлого прогона (`-pl ...`).
4. **Исключение модуля через `-pl '!…'`** ломает зависимых, если в репозитории нет артефакта текущей версии (после `git pull` версия поднялась 27→28).
5. **Ложный `[ERROR]`.** `gosdk-wrapper` пишет `[ERROR] Can't load GoSDK ... 403`, затем успешно качает с `go.dev`. Падение определять по Reactor Summary и `Failed to execute goal`, а не по `[ERROR]`.

## 7. Замеры

| Прогон | HEAD | Условия | Время | Итог |
|---|---|---|---|---|
| 1 | f139f13 | `~/.m2`, `-T 1C`, no-lint | 6:11 | 569 OK / 2 FAIL / 381 SKIP (из 952) |
| v2 | 2b42a56 | `.m2repo`, `-T 1.5C`, без clean, lint вкл. | 6:15 | упал на forbiddenapis (устаревший `target/`) |
| v3 | 2b42a56 | то же + no-lint | 28:56 | 506 OK / 2 FAIL / 451 SKIP |
| v4 | 2b42a56 | то же + `-fae -pl '!<2 grpc-модуля>'` | 25:27 | 825 OK / 4 FAIL / 128 SKIP (из 957) |
| drci (clean) | — | | — | не запускался: `mvn clean` не был разрешён в этой сессии |

## 8. Фикстуры (`testdata/maven-logs/`)

Email и домашний путь заменены (`user@example.com`, `/Users/dev`); середина логов обрезана, отмечено `# ... trimmed ...`. В начале каждого файла — строки `# buildyard fixture:` с описанием.

| Файл | Что внутри |
|---|---|
| `01-success-module-threaded.log` | начало реактора + все строки одного успешного модуля (`dremio-common-core`) с `[mvn-builder-…]` |
| `02-compile-error.log` | строки модуля `dremio-ee-sabot-kernel` + финальные `[ERROR]` компиляции (javac, `required/found/reason`) |
| `03-plugin-failure-quarkus-generate-code.log` | падение `quarkus-maven-plugin:generate-code` (x86 grpc-плагин) |
| `04-reactor-summary-failure-skipped.log` | Reactor Summary на 957 модулей с SUCCESS/FAILURE/SKIPPED, `BUILD FAILURE`, все ошибки (`-fae`) |
| `05-reactor-summary-plain-no-threadnames.log` | то же без префиксов потоков (обычный вывод `-T 1C`) |
| `06-false-error-gosdk-fallback.log` | перемешанный вывод `-T` с ложным `[ERROR]` GoSDK и успешным фолбэком |

## 9. Открытые вопросы

1. Как пользователь сейчас собирает 4 Quarkus-модуля с x86 grpc-плагином? Buildyard: помечать их как «нужен x86» и исключать вместе с зависимыми?
2. Прогнать `drci` (`clean install`, lint вкл.) и подтвердить, что ошибки устаревшего `target/` уходят.
3. `-T 1C` против `-T 1.5C` на 16 ядрах; повторный прогон с тёплым `.m2repo`.
4. mvnd: ставить ли для сравнения?
5. `-Dmaven.test.skip=true` (без компиляции тестов) против test-jar-зависимостей — не проверено.
