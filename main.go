package main

import (
	"fmt"
	"log"
	"net/http"
	"reflect"
	"strings"

	"github.com/flosch/pongo2"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	_ "modernc.org/sqlite"
)

var DB *gorm.DB

type Article struct {
	ID       uint   `form:"-"`
	Title    string `form:"Title"`
	Summary  string `form:"Summary,textarea"` // Mark as textarea
	Content  string `form:"Content,textarea"` // Mark as textarea
	Category string `form:"Category" label:"Category"`
	Private  int    `form:"PrivateSelect,select" label:"Private"`
	AuthorID uint   `form:"-"`                                          // Hidden in form
	Author   User   `gorm:"foreignKey:AuthorID;references:ID" form:"-"` // Hidden
	gorm.Model
}

type User struct {
	ID            uint   `form:"-"` // Excluded from the form
	FirstName     string `form:"FirstName" label:"First Name"`
	LastName      string `form:"LastName" label:"Last Name"`
	Pseudonym     string `form:"Pseudonym" label:"Pseudonym"`
	Mail          string `form:"Mail" label:"Email Address"`
	RoleRequested string `form:"RoleRequested" label:"User Role (Requested)"`
	Role          string `form:"Role" label:"User Role (Attributed)"`
	Password      string `form:"Password" label:"Password"`
	gorm.Model
}

const (
	Visitor uint = iota // 0
	Member              // 1
	Editor              // etc...
	EditorInChief
	Admin
)

func main() {

	Auth := map[string]uint{
		"Visitor":     Visitor, // 0
		"Member":      Member,  // 1
		"Contributor": Editor,  // etc...
		"Editor":      EditorInChief,
		"Admin":       Admin,
	}

	log.Printf("Auth : %v", Auth)

	r := gin.Default()
	initDatabase()

	// Setup Sessions and login
	store := cookie.NewStore([]byte("super-secret-key"))
	r.Use(sessions.Sessions("wiki_session", store))
	r.Use(LoadUser()) // before routes

	// ======
	// routes
	// ======

	r.Static("/public", "./public")

	// Existing article routes
	r.GET("/articles", getArticles)
	r.GET("/article/:id", getArticleByID)
	r.GET("/article_create", articleCreate)
	r.GET("/", home)
	r.GET("/users", getUsers)

	r.GET("/form_user", func(c *gin.Context) { generateForm[User](c) })
	r.POST("/create_user", func(c *gin.Context) { createRecord[User](c) })

	r.GET("/search_categories", searchCategories)

	r.POST("/update_article", func(c *gin.Context) { updateRecord[Article](c) })

	// Authorisations required
	r.GET("/form_article", AuthMiddleware(Editor), func(c *gin.Context) {
		generateForm[Article](c)
	})

	r.POST("/create_article", AuthMiddleware(Editor), func(c *gin.Context) {
		createRecord[Article](c)
	})

	r.GET("/edit_article/:id", AuthMiddleware(Editor), func(c *gin.Context) {
		generateEditForm[Article](c)
	})

	r.GET("/login", showLoginForm)
	r.POST("/login", performLogin)

	r.GET("/logout", performLogout)

	r.POST("/delete_article/:id", AuthMiddleware(Editor), deleteArticle)

	r.POST("/promote_user/:id", AuthMiddleware(Admin), promoteUser)

	r.Run(":3000")
}

// ============================
// End points implementations
// ==============================

func initDatabase() {
	var err error
	DB, err = gorm.Open(sqlite.Open("wiki.db"), &gorm.Config{})
	if err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}
	log.Println("Successfully connected to database.")

	// Migrate both Article and User tables
	DB.AutoMigrate(&Article{}, &User{})
}

func getArticles(c *gin.Context) {
	var userRole uint = Visitor // Default to Visitor

	// Map roles to access levels
	roleMap := map[string]uint{
		"Visitor":     Visitor,
		"Member":      Member,
		"Contributor": Editor,
		"Editor":      EditorInChief,
		"Admin":       Admin,
	}

	var currentUser *User
	if userAny, exists := c.Get("currentUser"); exists {
		user := userAny.(User)
		userRole = roleMap[user.Role]
		currentUser = &user
	}

	search := c.Query("search")
	query := DB.Order("lower(category) ASC, created_at DESC")

	// 🔐 Restrict access for Visitors
	if userRole == Visitor {
		query = query.Where("private = ?", false)
	}

	// 🔎 Apply search filter
	if search != "" {
		query = query.Where(
			"title LIKE ? OR summary LIKE ? OR content LIKE ?",
			"%"+search+"%", "%"+search+"%", "%"+search+"%",
		)
	}

	// 📥 Fetch matched articles
	var articles []Article
	if err := query.Find(&articles).Error; err != nil {
		c.String(500, "Database query failed: "+err.Error())
		return
	}

	// 📰 Fetch latest 5 (only when no search)
	var latestArticles []Article
	if search == "" {
		latestQuery := DB.Order("created_at DESC").Limit(5)
		if userRole == Visitor {
			latestQuery = latestQuery.Where("private = ?", false)
		}
		latestQuery.Find(&latestArticles)
	}

	// 📦 Group articles by category
	groupedArticles := make(map[string][]map[string]interface{})
	for _, article := range articles {
		groupedArticles[article.Category] = append(groupedArticles[article.Category], map[string]interface{}{
			"ID":        article.ID,
			"Title":     article.Title,
			"Summary":   article.Summary,
			"Category":  article.Category,
			"CreatedAt": article.CreatedAt.Format("2006-01-02 15:04"),
		})
	}

	// 📦 Format latest articles
	latestArticlesFormatted := []map[string]interface{}{}
	for _, article := range latestArticles {
		latestArticlesFormatted = append(latestArticlesFormatted, map[string]interface{}{
			"ID":        article.ID,
			"Title":     article.Title,
			"Summary":   article.Summary,
			"CreatedAt": article.CreatedAt.Format("2006-01-02 15:04"),
		})
	}

	// 🧠 Template context
	context := pongo2.Context{
		"grouped_articles": groupedArticles,
		"latest_articles":  latestArticlesFormatted,
		"search_query":     search,
	}

	if currentUser != nil {
		context["currentUser"] = *currentUser
	}

	tpl, _ := pongo2.FromFile("templates/articles.html")
	rendered, _ := tpl.Execute(context)

	c.Header("Content-Type", "text/html")
	c.String(200, rendered)
}

func getArticleByID(c *gin.Context) {
	var article Article
	id := c.Param("id")
	if err := DB.First(&article, id).Error; err != nil {
		c.String(http.StatusNotFound, "Article not found")
		return
	}

	context := pongo2.Context{
		"article": article,
		// Always define currentUser (even if nil)
		"currentUser": nil,
	}

	// If user is logged in, populate currentUser
	if userAny, exists := c.Get("currentUser"); exists {
		if user, ok := userAny.(User); ok {
			context["currentUser"] = user
		}
	}

	tpl, err := pongo2.FromFile("templates/article.html")
	if err != nil {
		log.Printf("Template load error: %v", err)
		c.String(http.StatusInternalServerError, "Template load error: "+err.Error())
		return
	}

	rendered, err := tpl.Execute(context)
	if err != nil {
		log.Printf("Template exec error: %v", err)
		c.String(http.StatusInternalServerError, "Template exec error: "+err.Error())
		return
	}

	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, rendered)
}

func articleCreate(c *gin.Context) {
	article := Article{
		ID:       10,
		Title:    "The_title",
		Content:  "The_Content",
		Summary:  "The_summary",
		Category: "the_category",
	}
	DB.Create(&article)
}

func home(c *gin.Context) {
	tpl, err := pongo2.FromFile("templates/home.html")
	if err != nil {
		log.Printf("Template load error: %v", err)
		c.String(http.StatusInternalServerError, "Template load failed")
		return
	}

	context := pongo2.Context{}

	// Always define currentUser, even if nil
	if userAny, exists := c.Get("currentUser"); exists {
		context["currentUser"] = userAny.(User)
	} else {
		context["currentUser"] = nil
	}

	rendered, err := tpl.Execute(context)
	if err != nil {
		log.Printf("Home template error: %v", err)
		c.String(http.StatusInternalServerError, "Template execution failed")
		return
	}

	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, rendered)

	tpl, err = pongo2.FromFile("templates/home.html")
	if err != nil {
		log.Printf("Template load error: %v", err)
		c.String(http.StatusInternalServerError, fmt.Sprintf("Template load failed: %v", err))
		return
	}

}

func extractFields[T any](excludeFields ...string) []map[string]string {
	var fields []map[string]string
	t := reflect.TypeOf(new(T)).Elem()

	exclude := map[string]bool{}
	for _, f := range excludeFields {
		exclude[f] = true
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		if field.Anonymous || exclude[field.Name] {
			continue
		}

		formTag := field.Tag.Get("form")
		labelTag := field.Tag.Get("label")

		if formTag == "-" || formTag == "Private_ui_only" {
			continue
		}

		fieldType := "text"
		fieldName := field.Name

		if formTag != "" {
			formParts := strings.Split(formTag, ",")
			fieldName = formParts[0]
			for _, part := range formParts {
				part = strings.TrimSpace(part)
				if part == "checkbox" {
					fieldType = "checkbox"
				} else if part == "textarea" {
					fieldType = "textarea"
				}
			}
		}

		label := field.Name
		if labelTag != "" {
			label = labelTag
		}

		if field.Name == "PrivateSelect" {
			fieldType = "select"
		}

		fields = append(fields, map[string]string{
			"name":  fieldName,
			"type":  fieldType,
			"label": label,
		})
	}

	return fields
}

func generateForm[T any](c *gin.Context) {
	tpl, err := pongo2.FromFile("templates/record_form.html")
	if err != nil {
		log.Printf("Template error: %v", err)
		c.String(http.StatusInternalServerError, "Template error: "+err.Error())
		return
	}

	context := pongo2.Context{
		"fields": extractFields[T]("Role"),
		"type":   reflect.TypeOf(new(T)).Elem().Name(),
	}

	// ✅ If generating an Article form, fetch existing categories
	if reflect.TypeOf(new(T)).Elem().Name() == "Article" {
		var categories []string
		DB.Model(&Article{}).Distinct("category").Pluck("category", &categories)
		context["categories"] = categories
	}

	rendered, err := tpl.Execute(context)
	if err != nil {
		log.Printf("Template execution error: %v", err)
		c.String(http.StatusInternalServerError, "Template execution error: "+err.Error())
		return
	}

	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, rendered)
}

func createRecord[T any](c *gin.Context) {
	var record T

	log.Printf("Raw Form Data: %+v", c.Request.PostForm)

	if err := c.ShouldBindWith(&record, binding.Form); err != nil {
		log.Printf("Binding error: %v", err)
		c.String(http.StatusBadRequest, "Binding error: "+err.Error())
		return
	}

	log.Printf("Updated Record: %+v", record)

	// 🔐 Secure defaults for Article
	if article, ok := any(&record).(*Article); ok {
		selectedCategory := c.PostForm("Category")
		newCategory := c.PostForm("new_category")

		if val := c.PostForm("PrivateSelect"); val == "1" {
			article.Private = 1
		} else {
			article.Private = 0
		}

		if newCategory != "" {
			article.Category = newCategory
		} else {
			article.Category = selectedCategory
		}
	}

	// 🔐 Secure defaults for User
	if user, ok := any(&record).(*User); ok {
		log.Printf("New user registration: %s (requested: %s)", user.Mail, user.RoleRequested)
		user.Role = "Member" // Force safe role regardless of form input
	}

	log.Printf("%T: %+v", record, record)

	if err := DB.Create(&record).Error; err != nil {
		log.Printf("Error creating record: %v", err)
		c.String(http.StatusInternalServerError, "Error creating record: "+err.Error())
		return
	}

	tpl, err := pongo2.FromFile("templates/record_success.html")
	if err != nil {
		log.Printf("Template error: %v", err)
		c.String(http.StatusInternalServerError, "Template error: "+err.Error())
		return
	}

	rendered, err := tpl.Execute(pongo2.Context{"record": record})
	if err != nil {
		log.Printf("Template execution error: %v", err)
		c.String(http.StatusInternalServerError, "Template execution error: "+err.Error())
		return
	}

	c.Request.ParseForm() // Ensure form data is parsed
	log.Printf("\nReceived form data: %+v\n", c.Request.PostForm)

	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, rendered)
}

func getUsers(c *gin.Context) {
	var users []User
	if err := DB.Find(&users).Error; err != nil {
		c.String(500, "Database query failed: "+err.Error())
		return
	}

	tpl, err := pongo2.FromFile("templates/list_users.html")
	if err != nil {
		c.String(500, "Template load error: "+err.Error())
		return
	}

	context := pongo2.Context{
		"users": users,
		"roles": []string{"Member", "Contributor", "Editor", "Admin"},
	}

	if userAny, exists := c.Get("currentUser"); exists {
		context["currentUser"] = userAny.(User)
	}

	rendered, err := tpl.Execute(context)
	if err != nil {
		c.String(500, "Template exec error: "+err.Error())
		return
	}

	c.Header("Content-Type", "text/html")
	c.String(200, rendered)
}

func searchCategories(c *gin.Context) {
	query := c.Query("q")                 // ✅ HTMX sends query under `q`, not `Category`
	log.Printf("Search Query: %s", query) // ✅ Debug log

	var categories []string
	if query != "" {
		DB.Model(&Article{}).
			Where("LOWER(category) LIKE LOWER(?)", "%"+query+"%"). // ✅ Fix filtering
			Distinct("category").
			Pluck("category", &categories)
	} else {
		DB.Model(&Article{}).Distinct("category").Pluck("category", &categories)
	}

	log.Printf("Matching Categories: %+v", categories) // ✅ Debug log

	var suggestions string
	for _, category := range categories {
		suggestions += fmt.Sprintf(
			`<li hx-on:click="document.getElementById('category_search').value='%s'; document.getElementById('category_suggestions').innerHTML='';">%s</li>`,
			category, category)
	}

	c.String(200, suggestions) // ✅ Return the filtered category list
}

func generateEditForm[T any](c *gin.Context) {
	id := c.Param("id")
	var record T

	// Load existing record from DB by ID
	if err := DB.First(&record, id).Error; err != nil {
		c.String(http.StatusNotFound, "Record not found: "+err.Error())
		return
	}

	fields := extractFields[T]()
	v := reflect.ValueOf(record)

	// Pre-fill values
	for i := range fields {
		field := v.FieldByName(fields[i]["name"])
		if field.IsValid() && field.CanInterface() {
			fields[i]["value"] = fmt.Sprintf("%v", field.Interface())
		}
	}

	tpl, err := pongo2.FromFile("templates/record_form.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "Template error: "+err.Error())
		return
	}

	context := pongo2.Context{
		"fields": fields,
		"type":   reflect.TypeOf(new(T)).Elem().Name(),
		"isEdit": true,
		"ID":     id,
	}

	if article, ok := any(record).(Article); ok {
		context["PrivateSelect"] = fmt.Sprintf("%d", article.Private)
	}

	rendered, _ := tpl.Execute(context)
	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, rendered)
}

func updateRecord[T any](c *gin.Context) {
	id := c.PostForm("ID")
	if id == "" {
		c.String(http.StatusBadRequest, "Missing ID")
		return
	}

	record := new(T)

	// Load existing record by ID
	if err := DB.First(record, id).Error; err != nil {
		c.String(http.StatusNotFound, "Record not found: "+err.Error())
		return
	}

	// Bind all standard fields
	if err := c.ShouldBindWith(record, binding.Form); err != nil {
		log.Printf("Form bind error: %v", err)
		c.String(http.StatusBadRequest, fmt.Sprintf("Form bind error: %v", err))
		return
	}

	// 🧪 Custom handling for Article-specific fields
	if rec, ok := any(record).(*Article); ok {

		if val := c.PostForm("PrivateSelect"); val == "1" {
			rec.Private = 1
		} else {
			rec.Private = 0
		}

		// ✅ Category override from autocomplete input
		newCategory := c.PostForm("new_category")
		if newCategory != "" {
			rec.Category = newCategory
		}

		log.Printf("✅ Final Article.Private: %v", rec.Private)
	}

	// ✅ Save changes to DB
	if err := DB.Save(record).Error; err != nil {
		c.String(http.StatusInternalServerError, "Update failed: "+err.Error())
		return
	}

	c.String(http.StatusOK, "Record updated successfully!")
}

// Login Handlers
func showLoginForm(c *gin.Context) {
	tpl, _ := pongo2.FromFile("templates/login.html")
	html, _ := tpl.Execute(pongo2.Context{})
	c.Header("Content-Type", "text/html")
	c.String(200, html)
}

func performLogin(c *gin.Context) {
	email := c.PostForm("email")
	password := c.PostForm("password")

	var user User
	if err := DB.Where("mail = ? AND password = ?", email, password).First(&user).Error; err != nil {
		c.String(401, "Invalid credentials")
		return
	}

	session := sessions.Default(c)
	session.Set("userID", user.ID)
	session.Save()

	c.Redirect(302, "/articles")
}

// Middleware to Load User From Session
func AuthMiddleware(requiredRole uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		userID := session.Get("userID")

		if userID == nil {
			c.String(401, "Unauthorized: Please login")
			c.Abort()
			return
		}

		var user User
		if err := DB.First(&user, userID).Error; err != nil {
			c.String(401, "Unauthorized: Invalid session")
			c.Abort()
			return
		}

		roleLevel := map[string]uint{
			"Visitor":     Visitor,
			"Member":      Member,
			"Contributor": Editor,
			"Editor":      EditorInChief,
			"Admin":       Admin,
		}

		userRole := roleLevel[user.Role]
		if userRole < requiredRole {
			c.String(403, "Forbidden: Insufficient role")
			c.Abort()
			return
		}

		// Store user in context for future handlers
		c.Set("currentUser", user)
		c.Next()
	}
}

func performLogout(c *gin.Context) {
	session := sessions.Default(c)
	session.Clear()
	session.Save()

	c.Redirect(302, "/login")
}

func LoadUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		userID := session.Get("userID")
		if userID != nil {
			var user User
			if err := DB.First(&user, userID).Error; err == nil {
				c.Set("currentUser", user)
			}
		}
		c.Next()
	}
}

// Utility to test templates.
func testTemplate() {
	tpl, err := pongo2.FromFile("templates/links.html")
	if err != nil {
		log.Fatalf("Syntax error: %v", err)
	}

	out, err := tpl.Execute(pongo2.Context{"currentUser": User{Role: "Admin"}})
	if err != nil {
		log.Fatalf("Execution error: %v", err)
	}
	fmt.Println(out)
}

func deleteArticle(c *gin.Context) {
	id := c.Param("id")

	var article Article
	if err := DB.First(&article, id).Error; err != nil {
		c.String(http.StatusNotFound, "Article not found")
		return
	}

	if err := DB.Delete(&article).Error; err != nil {
		c.String(http.StatusInternalServerError, "Delete failed: "+err.Error())
		return
	}

	log.Printf("🗑️ Deleted article with ID: %s", id)
	c.Redirect(http.StatusFound, "/articles")
}

func promoteUser(c *gin.Context) {
	id := c.Param("id")
	newRole := c.PostForm("new_role")

	var user User
	if err := DB.First(&user, id).Error; err != nil {
		c.String(http.StatusNotFound, "User not found")
		return
	}

	log.Printf("🔼 Promoting user %s to %s", user.Mail, newRole)
	user.Role = newRole

	if err := DB.Save(&user).Error; err != nil {
		c.String(http.StatusInternalServerError, "Failed to update role: "+err.Error())
		return
	}

	c.Redirect(302, "/users")
}
