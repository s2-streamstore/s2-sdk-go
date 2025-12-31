package s2

import "log/slog"

func logInfo(logger *slog.Logger, msg string, attrs ...any) {
	if logger == nil {
		return
	}
	logger.Info(msg, attrs...)
}

func logError(logger *slog.Logger, msg string, attrs ...any) {
	if logger == nil {
		return
	}
	logger.Error(msg, attrs...)
}

func logDebug(logger *slog.Logger, msg string, attrs ...any) {
	if logger == nil {
		return
	}

	logger.Debug(msg, attrs...)
}
