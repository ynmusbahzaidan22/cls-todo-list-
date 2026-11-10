package task

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxTemplateItems = 50
	MaxTemplateName  = 100
)

type TemplateItem struct {
	ID          string `json:"id,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
	DueInDays   *int   `json:"due_in_days,omitempty"`
}

type Template struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Version     int64          `json:"version"`
	Items       []TemplateItem `json:"items"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type TemplateSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Version     int64     `json:"version"`
	ItemCount   int       `json:"item_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type TemplateRepository interface {
	CreateTemplate(context.Context, string, string, string, []TemplateItem) (Template, error)
	ListTemplates(context.Context, string) ([]TemplateSummary, error)
	GetTemplate(context.Context, string, string) (Template, error)
	UpdateTemplate(context.Context, string, string, int64, string, string, []TemplateItem) (Template, error)
	DeleteTemplate(context.Context, string, string, int64) error
	ApplyTemplate(context.Context, string, string, int64, bool, ...string) ([]Task, error)
}

type TemplateService struct {
	repository TemplateRepository
}

func NewTemplateService(repository TemplateRepository) *TemplateService {
	return &TemplateService{repository: repository}
}

func (s *TemplateService) Create(ctx context.Context, userID, name, description string, items []TemplateItem) (Template, error) {
	cleanName, cleanDescription, cleanItems, err := validateTemplate(name, description, items)
	if err != nil {
		return Template{}, err
	}
	for _, item := range cleanItems {
		if item.ID != "" {
			return Template{}, ErrInvalidInput
		}
	}
	created, err := s.repository.CreateTemplate(ctx, userID, cleanName, cleanDescription, cleanItems)
	if err != nil {
		return Template{}, fmt.Errorf("create task template: %w", mapRepositoryError("create task template", err))
	}
	return created, nil
}

func (s *TemplateService) List(ctx context.Context, userID string) ([]TemplateSummary, error) {
	items, err := s.repository.ListTemplates(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list task templates: %w", mapRepositoryError("list task templates", err))
	}
	if items == nil {
		items = []TemplateSummary{}
	}
	return items, nil
}

func (s *TemplateService) Get(ctx context.Context, userID, templateID string) (Template, error) {
	if !ValidID(templateID) {
		return Template{}, ErrInvalidInput
	}
	template, err := s.repository.GetTemplate(ctx, userID, strings.ToLower(templateID))
	if err != nil {
		return Template{}, mapRepositoryError("get task template", err)
	}
	return template, nil
}

func (s *TemplateService) Update(ctx context.Context, userID, templateID string, version int64, name, description string, items []TemplateItem) (Template, error) {
	if !ValidID(templateID) || version < 1 {
		return Template{}, ErrInvalidInput
	}
	cleanName, cleanDescription, cleanItems, err := validateTemplate(name, description, items)
	if err != nil {
		return Template{}, err
	}
	updated, err := s.repository.UpdateTemplate(ctx, userID, strings.ToLower(templateID), version, cleanName, cleanDescription, cleanItems)
	if err != nil {
		return Template{}, mapRepositoryError("update task template", err)
	}
	return updated, nil
}

func (s *TemplateService) Delete(ctx context.Context, userID, templateID string, version int64) error {
	if !ValidID(templateID) || version < 1 {
		return ErrInvalidInput
	}
	if err := s.repository.DeleteTemplate(ctx, userID, strings.ToLower(templateID), version); err != nil {
		return mapRepositoryError("delete task template", err)
	}
	return nil
}

func (s *TemplateService) Apply(ctx context.Context, userID, templateID string, version int64, newSet bool, timeZones ...string) ([]Task, error) {
	if !ValidID(templateID) || version < 1 || len(timeZones) > 1 || (len(timeZones) == 1 && !ValidTimeZone(timeZones[0])) {
		return nil, ErrInvalidInput
	}
	created, err := s.repository.ApplyTemplate(ctx, userID, strings.ToLower(templateID), version, newSet, timeZones...)
	if err != nil {
		return nil, mapRepositoryError("apply task template", err)
	}
	return created, nil
}

func validateTemplate(name, description string, items []TemplateItem) (string, string, []TemplateItem, error) {
	cleanName, err := validateTemplateName(name)
	if err != nil || validateDescription(description) != nil || len(items) == 0 || len(items) > MaxTemplateItems {
		return "", "", nil, ErrInvalidInput
	}
	cleanItems := make([]TemplateItem, len(items))
	seenTitles := make(map[string]struct{}, len(items))
	seenIDs := make(map[string]struct{}, len(items))
	for index, item := range items {
		cleanTitle, titleErr := validateTitle(item.Title)
		if item.Priority == "" {
			item.Priority = PriorityMedium
		}
		if titleErr != nil || (item.ID != "" && !ValidID(item.ID)) || validateDescription(item.Description) != nil || !validPriority(item.Priority) || (item.DueInDays != nil && (*item.DueInDays < 0 || *item.DueInDays > 3650)) {
			return "", "", nil, ErrInvalidInput
		}
		key := strings.ToLower(cleanTitle)
		if _, duplicate := seenTitles[key]; duplicate {
			return "", "", nil, ErrInvalidInput
		}
		seenTitles[key] = struct{}{}
		if item.ID != "" {
			if _, duplicate := seenIDs[strings.ToLower(item.ID)]; duplicate {
				return "", "", nil, ErrInvalidInput
			}
			seenIDs[strings.ToLower(item.ID)] = struct{}{}
		}
		cleanItems[index] = TemplateItem{ID: strings.ToLower(item.ID), Title: cleanTitle, Description: item.Description, Priority: item.Priority, DueInDays: item.DueInDays}
	}
	return cleanName, description, cleanItems, nil
}

func validateTemplateName(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", ErrInvalidInput
	}
	clean := strings.TrimSpace(value)
	if length := utf8.RuneCountInString(clean); length < 1 || length > MaxTemplateName || strings.IndexByte(clean, 0) >= 0 {
		return "", ErrInvalidInput
	}
	return clean, nil
}
