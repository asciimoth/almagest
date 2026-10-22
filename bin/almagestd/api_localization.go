package main

import (
	"errors"
	"os"

	"github.com/asciimoth/almagest/bin/almagestd/localizedcontent"
	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/pasta/pasta"
	"github.com/kataras/golog"
)

func (a *AdminAPI) prepareLocalizedContent(defaultRoot string) error {
	root, err := localizedcontent.ResolveRoot(defaultRoot)
	if err != nil {
		return err
	}
	reader, err := localizedcontent.New(root, golog.Warnf)
	if err != nil {
		var pathError *os.PathError
		if errors.Is(err, os.ErrNotExist) || errors.As(err, &pathError) {
			golog.Warnf("Cannot access localized content at %s: %v; using built-in text", root, err)
			return nil
		}
		return err
	}
	a.localizedContent = reader
	return nil
}

func (a *AdminAPI) prepareExamples(defaultRoot string) error {
	root, err := resolveExamplesRoot(defaultRoot)
	if err != nil {
		return err
	}
	store, err := openExampleStore(root)
	if err != nil {
		var pathError *os.PathError
		if errors.Is(err, os.ErrNotExist) || errors.As(err, &pathError) {
			golog.Warnf("Cannot access examples at %s: %v; no examples will be available", root, err)
			return nil
		}
		return err
	}
	a.examples = store
	return nil
}

func (s *apiSession) currentLocale() string {
	s.localeMu.RLock()
	defer s.localeMu.RUnlock()
	return s.locale
}

func (s *apiSession) localeSnapshot() api.LocaleSnapshot {
	requested := s.currentLocale()
	result := api.LocaleSnapshot{Requested: requested, Effective: "en", Panel: map[string]string{}}
	if s.admin.localizedContent == nil {
		result.Available = []api.AvailableLocale{{Locale: "en", Name: "English"}}
		return result
	}
	result.Effective = s.admin.localizedContent.EffectiveLocale(requested)
	result.Panel = s.admin.localizedContent.Panel(requested)
	result.Revision = s.admin.localizedContent.Revision()
	for _, locale := range s.admin.localizedContent.AvailableLocales() {
		result.Available = append(result.Available, api.AvailableLocale{Locale: locale.Locale, Name: locale.Name})
	}
	return result
}

func (s *apiSession) localizeClass(locale string, class *api.NodeClassSnapshot) {
	if class == nil || s.admin.localizedContent == nil {
		return
	}
	class.ShortDescription, _ = s.admin.localizedContent.NodeShort(locale, class.Class, class.ShortDescription)
}

func (s *apiSession) localizeClasses(locale string, classes []api.NodeClassSnapshot) {
	for index := range classes {
		s.localizeClass(locale, &classes[index])
	}
}

func (s *apiSession) localizeNotification(locale string, notification almagest.EngineNotification) almagest.EngineNotification {
	if notification.Workspace == nil || s.admin.localizedContent == nil {
		return notification
	}
	workspace := *notification.Workspace
	notification.Workspace = &workspace
	if workspace.NodeClass != nil {
		class := *workspace.NodeClass
		class.ShortDescription, _ = s.admin.localizedContent.NodeShort(locale, class.Class, class.ShortDescription)
		workspace.NodeClass = &class
	}
	if workspace.Snapshot != nil {
		snapshot := *workspace.Snapshot
		snapshot.Classes = make(map[string]pasta.NodeClassSnapshot, len(workspace.Snapshot.Classes))
		for name, class := range workspace.Snapshot.Classes {
			class.ShortDescription, _ = s.admin.localizedContent.NodeShort(locale, class.Class, class.ShortDescription)
			snapshot.Classes[name] = class
		}
		workspace.Snapshot = &snapshot
	}
	if workspace.Kind == pasta.NotificationNodeMenu {
		if node, ok := s.engine.NodeSnapshot(notification.WorkspaceID, workspace.ID); ok {
			workspace.Formular = s.admin.localizedContent.TranslateFormular(locale, node.Class, workspace.Formular)
		}
	}
	return notification
}
