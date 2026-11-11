package task

import (
	"strings"
	"testing"
)

func TestValidateTemplateBoundaries(t *testing.T) {
	valid := []TemplateItem{{Title: "  First task  "}, {Title: "Second task", Description: "details"}}
	name, description, items, err := validateTemplate("  Weekly prep  ", "  ", valid)
	if err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	if name != "Weekly prep" || description != "  " || items[0].Title != "First task" {
		t.Fatalf("unexpected normalized template: %#v %q", items, name)
	}
	if items[0].Priority != PriorityMedium || items[1].Priority != PriorityMedium {
		t.Fatalf("omitted template item priorities were not defaulted: %#v", items)
	}
	_, _, explicit, err := validateTemplate("Priority template", "", []TemplateItem{{Title: "Urgent", Priority: PriorityUrgent}})
	if err != nil || explicit[0].Priority != PriorityUrgent {
		t.Fatalf("explicit template priority not preserved: %#v, %v", explicit, err)
	}

	cases := []struct {
		name         string
		templateName string
		items        []TemplateItem
	}{
		{name: "empty name", templateName: " ", items: valid},
		{name: "too long name", templateName: strings.Repeat("n", MaxTemplateName+1), items: valid},
		{name: "no items", templateName: "Weekly", items: nil},
		{name: "duplicate case-insensitive title", templateName: "Weekly", items: []TemplateItem{{Title: "Work"}, {Title: " work "}}},
		{name: "invalid title", templateName: "Weekly", items: []TemplateItem{{Title: " "}}},
		{name: "invalid priority", templateName: "Weekly", items: []TemplateItem{{Title: "Task", Priority: "ASAP"}}},
		{name: "too many items", templateName: "Weekly", items: makeItems(MaxTemplateItems + 1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := validateTemplate(test.templateName, "", test.items); err == nil {
				t.Fatal("expected invalid template to be rejected")
			}
		})
	}
	if _, _, _, err := validateTemplate("Maximum", "", makeItems(MaxTemplateItems)); err != nil {
		t.Fatalf("maximum-size template rejected: %v", err)
	}
}

func makeItems(count int) []TemplateItem {
	items := make([]TemplateItem, count)
	for i := range items {
		items[i].Title = "Task " + strings.Repeat("x", i/10) + string(rune('A'+i%26)) + string(rune('0'+i/26))
	}
	return items
}
