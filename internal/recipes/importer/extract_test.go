package importer

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) *Document {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".html")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://example.com/recipes/" + name)
	d, err := ParseDocument(b, u)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Fixtures are real pages trimmed to their <head> metadata and JSON-LD
// (fetched 2026-11-27).
func TestJSONLDFixtures(t *testing.T) {
	cases := []struct {
		file, title, firstIng, lang string
		servings, minutes           int
		ings, steps                 int
		stepPrefix                  string
	}{
		{"bbcgoodfood", "Easy chicken curry", "2 tbsp sunflower oil", "en", 4, 50, 11, 4, "Heat the oil"},
		{"bonappetit", "BA’s Best Chocolate Chip Cookies", "1½ cups plus 1 Tbsp. (200 g) all-purpose flour (spooning into measuring cups, then leveling)", "en", 16, 0, 10, 6, "Place racks"},
		{"cafedelites", "Chicken Tikka Masala Recipe", "28 oz boneless chicken thighs (skinless, cut into bite-sized pieces)", "en", 6, 45, 26, 6, "In a bowl"}, // @graph
		{"loveandlemons", "BEST Hummus", "1½ cups cooked chickpeas (drained and rinsed)", "en", 8, 5, 9, 2, "In a high-speed blender"},
		{"marmiton", "Pâte à crêpes simple : la meilleure recette", "5 oeufs", "fr", 20, 45, 5, 3, "Dans un saladier"},
		{"pinchofyum", "The Best Soft Chocolate Chip Cookies", "8 tablespoons of salted butter", "en", 12, 20, 9, 5, "Preheat the oven"},
		{"recipetineats", "Chicken Tikka Masala", "600g/ 1.2lb chicken thigh ((boneless, skinless), cut into 3cm / 2.2\" cubes (Note 1))", "en", 4, 60, 30, 16, "Chicken Tikka: Combine"}, // HowToSection
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			doc := fixture(t, c.file)
			r := doc.Recipe()
			if r == nil {
				t.Fatal("no recipe")
			}
			if r.Via != "jsonld" || r.Title != c.title || r.Ingredients[0] != c.firstIng {
				t.Errorf("via %s title %q first %q", r.Via, r.Title, r.Ingredients[0])
			}
			if deref(r.Servings) != c.servings || deref(r.TotalMinutes) != c.minutes {
				t.Errorf("servings %d minutes %d", deref(r.Servings), deref(r.TotalMinutes))
			}
			if len(r.Ingredients) != c.ings || len(r.Steps) != c.steps {
				t.Errorf("%d ingredients, %d steps", len(r.Ingredients), len(r.Steps))
			}
			if !strings.HasPrefix(r.Steps[0].Text, c.stepPrefix) {
				t.Errorf("step 1 = %q", r.Steps[0].Text)
			}
			if !strings.HasPrefix(r.ImageURL, "https://") {
				t.Errorf("image %q", r.ImageURL)
			}
			if got := draftLang(r, doc.Lang); got != c.lang {
				t.Errorf("lang %q", got)
			}
			for _, s := range r.Steps {
				if strings.Contains(s.Text, "<") || strings.Contains(s.Text, "&amp;") {
					t.Errorf("markup left in %q", s.Text)
				}
			}
		})
	}
}

func TestMicrodata(t *testing.T) {
	page := `<!doctype html><html lang="en"><body>
	<div itemscope itemtype="https://schema.org/Recipe">
	  <h1 itemprop="name">Lentil soup</h1>
	  <img itemprop="image" src="/img/soup.jpg">
	  <meta itemprop="totalTime" content="PT1H5M">
	  <span itemprop="recipeYield">Serves 6</span>
	  <ul><li itemprop="recipeIngredient">1 cup red lentils</li><li itemprop="recipeIngredient">1 onion, diced</li></ul>
	  <div itemprop="author" itemscope itemtype="https://schema.org/Person"><span itemprop="name">Sam</span></div>
	  <ol>
	    <li itemprop="recipeInstructions" itemscope itemtype="https://schema.org/HowToStep"><span itemprop="text">Sweat the onion.</span></li>
	    <li itemprop="recipeInstructions" itemscope itemtype="https://schema.org/HowToStep"><span itemprop="text">Add lentils &amp; water; simmer.</span></li>
	  </ol>
	</div></body></html>`
	u, _ := url.Parse("https://soup.example/r/1")
	doc, err := ParseDocument([]byte(page), u)
	if err != nil {
		t.Fatal(err)
	}
	r := doc.Recipe()
	if r == nil || r.Via != "microdata" {
		t.Fatalf("got %+v", r)
	}
	if r.Title != "Lentil soup" || deref(r.Servings) != 6 || deref(r.TotalMinutes) != 65 || r.ImageURL != "https://soup.example/img/soup.jpg" {
		t.Errorf("got %+v", r)
	}
	if len(r.Ingredients) != 2 || len(r.Steps) != 2 || r.Steps[1].Text != "Add lentils & water; simmer." {
		t.Errorf("ingredients %v steps %v", r.Ingredients, r.Steps)
	}
}

func TestLDShapes(t *testing.T) {
	page := `<html><head><script type="application/ld+json">
	//<![CDATA[
	[{"@type":"WebSite"},{"@type":["Recipe","NewsArticle"],"name":"Fesenjan &amp; rice","inLanguage":"fa-IR",
	  "recipeYield":["4","4 servings"],"prepTime":"PT20M","cookTime":"PT1H30M",
	  "image":[{"@type":"ImageObject","url":"https://img.example/f.jpg"}],
	  "recipeIngredient":"1 kg chicken",
	  "recipeInstructions":"Toast the walnuts.<br>Simmer with pomegranate molasses.\n\nServe."}]
	//]]>
	</script></head></html>`
	doc, err := ParseDocument([]byte(page), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := doc.Recipe()
	if r == nil || r.Title != "Fesenjan & rice" || r.Lang != "fa" || deref(r.Servings) != 4 || deref(r.TotalMinutes) != 110 {
		t.Fatalf("got %+v", r)
	}
	if len(r.Steps) != 3 || r.Ingredients[0] != "1 kg chicken" || r.ImageURL != "https://img.example/f.jpg" {
		t.Fatalf("steps %v ingredients %v image %q", r.Steps, r.Ingredients, r.ImageURL)
	}
	// Latin text tagged fa: the text decides.
	if got := draftLang(r, ""); got != "en" {
		t.Errorf("lang = %q", got)
	}
}

func TestMinutes(t *testing.T) {
	for in, want := range map[string]int{"PT1H30M": 90, "P0DT0H20M": 20, "PT45M": 45, "PT90S": 2, "P1D": 1440, "PT0.5H": 30} {
		if got, ok := Minutes(in); !ok || got != want {
			t.Errorf("%s = %d, %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "P", "PT", "30 minutes", "1:30"} {
		if _, ok := Minutes(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestMainText(t *testing.T) {
	page := `<html><head><title>Nan-e berenji</title></head><body>
	<nav>Home Recipes Shop</nav><header>Site</header>
	<article><h1>Nan-e berenji</h1><p>Rice-flour cookies with rosewater.</p>
	<ul><li>2 cups rice flour</li><li>1 cup sugar</li></ul>
	<div class="comments">Great!</div><script>track()</script></article>
	<footer>© 2026</footer></body></html>`
	doc, _ := ParseDocument([]byte(page), nil)
	got := doc.MainText()
	want := "Nan-e berenji\nRice-flour cookies with rosewater.\n2 cups rice flour\n1 cup sugar"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestDetectScript(t *testing.T) {
	for in, want := range map[string]string{
		"خورش قیمه با سیب\u200cزمینی سرخ\u200cکرده":                                                             "fa",
		"Борщ со сметаной и чесноком":                                                                           "ru",
		"Chicken curry with rice":                                                                               "",
		"Khoresh gheymeh (خورش قیمه) is a Persian stew with split peas, lamb and dried limes, served over rice": "",
		"親子丼の作り方とコツ":                                                                                            "ja",
		"ab":                                                                                                    "",
	} {
		if got := DetectScript(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}

func TestSplitStructured(t *testing.T) {
	text := `Weeknight dal

Ingredients
1 cup red lentils
1 onion, chopped
2 tsp cumin seeds
Salt

Method
1. Rinse the lentils.
2. Fry the onion and cumin, add lentils and 3 cups water.
3. Simmer 20 minutes.

Notes
Freezes well.`
	d := SplitStructured(text)
	if d == nil {
		t.Fatal("not structured")
	}
	if d.Title != "Weeknight dal" || len(d.Ingredients) != 4 || len(d.Steps) != 3 || d.Notes != "Freezes well." {
		t.Fatalf("got %+v", d)
	}
	if SplitStructured("so I made this amazing dal last night, you fry onions then add lentils, it was great") != nil {
		t.Fatal("prose split")
	}
	fa := "خورش قیمه\nمواد لازم\n۵۰۰ گرم گوشت\n۱ پیمانه لپه\n۲ عدد پیاز\nطرز تهیه\n۱. پیاز را سرخ کنید.\n۲. گوشت را اضافه کنید."
	d = SplitStructured(fa)
	if d == nil || len(d.Ingredients) != 3 || len(d.Steps) != 2 || draftLang(d, "") != "fa" {
		t.Fatalf("persian: %+v", d)
	}
}

func TestCanonicalReel(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.instagram.com/reel/C9xAbc_12-/?igsh=abc": "https://www.instagram.com/reel/C9xAbc_12-/",
		"https://instagram.com/p/DAbCdEf123/":                 "https://www.instagram.com/reel/DAbCdEf123/",
		"https://www.instagram.com/chef.sam/reel/C9xAbc12/":   "https://www.instagram.com/reel/C9xAbc12/",
	} {
		if got, err := canonicalReel(in); err != nil || got != want {
			t.Errorf("%s = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"https://www.instagram.com/chef.sam/", "https://evil.example/reel/abcdef/", "https://www.instagram.com/reel/--exec/"} {
		if got, err := canonicalReel(bad); err == nil && !strings.HasPrefix(got, "https://www.instagram.com/reel/") {
			t.Errorf("%s accepted as %q", bad, got)
		}
	}
	if !IsInstagram("https://www.instagram.com/reel/x") || IsInstagram("https://instagram.com.evil.example/reel/x") {
		t.Error("IsInstagram")
	}
}
