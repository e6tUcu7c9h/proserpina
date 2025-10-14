# Changelog

## [Unreleased]
### Added
- SQLite insertion and export operations
- Jira now fetches with changelog
- Tests suite

### Changed
- SQLite helpers: RunQuery and RunQueryFolder now validate database file path length and ensure parent directory exists before opening the DB
- ExportTable: validates db and CSV path lengths and ensures parent directory exists for CSV output as well as returns an error object
- The number of concurrent fetches for jira is now a parameter

## [0.1.1] - 2024-11-14
### Added
- Repository initialization


[unreleased]: https://github.com/e6tUcu7c9h/proserpina
[0.1.1]: https://github.com/e6tUcu7c9h/proserpina/tree/v0.1.1
