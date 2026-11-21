cmake_minimum_required(VERSION 3.24)
project(GooglePhotosFS VERSION 0.2.0 LANGUAGES NONE)

find_program(GO_EXECUTABLE NAMES go REQUIRED)
file(GLOB GO_SOURCES CONFIGURE_DEPENDS "${CMAKE_CURRENT_SOURCE_DIR}/src/*.go")
set(EXECUTABLE_PATH "${CMAKE_CURRENT_BINARY_DIR}/bin/googlephotosfs.exe")

add_custom_command(
    OUTPUT "${EXECUTABLE_PATH}"
    COMMAND "${CMAKE_COMMAND}" -E make_directory "${CMAKE_CURRENT_BINARY_DIR}/bin"
    COMMAND "${CMAKE_COMMAND}" -E env "GOOS=windows" "GOARCH=amd64" "CGO_ENABLED=0"
        "${GO_EXECUTABLE}" build -buildvcs=false -trimpath "-ldflags=-s -w" -o "${EXECUTABLE_PATH}" ./src
    DEPENDS ${GO_SOURCES} go.mod go.sum
    WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}"
    VERBATIM
)

add_custom_target(GooglePhotosFSWindows ALL DEPENDS "${EXECUTABLE_PATH}")
install(PROGRAMS "${EXECUTABLE_PATH}" DESTINATION .)
install(FILES README.md VALIDACION.md NOTICE googlephotosfs.toml DESTINATION .)
install(DIRECTORY licenses DESTINATION .)
